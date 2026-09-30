package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Опознание контроллеров СКУД Z5R WEB BT при сканировании сети.
//
// Зачем это нужно. Контроллер — устройство того же класса, что и камера:
// у него есть веб-интерфейс и адрес в сети. Оператор, зайдя в сканер,
// ожидает увидеть всё оборудование, а не только камеры. Без отдельной
// проверки контроллер либо отсеивался (у него нет RTSP), либо попадал
// в список как камера неизвестного производителя — и то и другое неверно.
//
// Признаки контроллера:
//
//   - есть веб-сервер на 80-м порту, но нет RTSP;
//   - страница отдаёт имя `Z5R-WEB` и/или производителя `ironlogic.ru`;
//   - эндпоинт /stat существует и возвращает JSON с серийным номером.
//
// Последний признак самый надёжный: других устройств с таким набором
// полей в сети не встречается, и он же подтверждает, что перед нами
// именно этот контроллер, а не просто страница с похожим названием.

// z5rScanCreds — учётные данные, с которыми пробуем опознать контроллер.
//
// Контроллер не отдаёт /stat без авторизации, поэтому одной проверки
// страницы мало. Перебор ограничен: у контроллера нет привычных заводских
// пар вида admin/admin, его пароль задаётся при настройке, но иногда
// оставляют пустым — это и проверяем.
var z5rScanCreds = []credentialPair{
	{"", ""},
	{"z5rweb", "z5rweb"},
}

// probeZ5R пытается опознать контроллер Z5R WEB BT по адресу.
//
// Возвращает nil, если устройство не является контроллером этого типа.
func (s *CameraScanner) probeZ5R(ctx context.Context, ip string) *domain.DiscoveredCamera {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()

	// Сначала смотрим страницу: она отдаёт название устройства без пароля.
	// Это дешёвая проверка, отсекающая большинство посторонних устройств.
	if !s.looksLikeZ5R(probeCtx, ip) {
		return nil
	}

	// Страница совпала — уточняем по /stat. Он требует пароля, поэтому
	// пробуем те учётные данные, что передал оператор, а затем стандартные.
	creds := make([]credentialPair, 0, len(z5rScanCreds)+1)
	if u, p := credentialHint(ctx); u != "" {
		creds = append(creds, credentialPair{u, p})
	}
	creds = append(creds, z5rScanCreds...)

	for _, cred := range creds {
		st, ok := s.fetchZ5RStat(probeCtx, ip, cred)
		if !ok {
			continue
		}

		mac := s.getMAC(ip)
		log.Info().Str("ip", ip).Str("sn", st.SN).
			Str("ctrlfw", st.CtrlFW).
			Msg("найден контроллер СКУД Z5R WEB BT")

		return &domain.DiscoveredCamera{
			IP:         ip,
			MAC:        mac,
			Vendor:     "z5r",
			VendorName: "Z5R WEB BT (IronLogic)",
			Firmware:   st.CtrlFW,
			// В поле модели кладём серийный номер: у контроллера нет
			// модели как таковой, а серийник — единственное, чем одно
			// устройство отличается от другого. Оператор увидит его
			// в списке и сможет отличить два контроллера в одной сети.
			Model:    "SN " + st.SN,
			HowFound: "по веб-интерфейсу Z5R-WEB",
			// Логин и пароль сохраняем, только если они подошли: тогда
			// заведение контроллера в систему пройдёт без ручного ввода.
			Username: cred.Username,
			Password: cred.Password,
			Online:   true,
			// Потоков у контроллера нет, поля остаются пустыми: это
			// не камера, и предлагать RTSP-адреса было бы ошибкой.
		}
	}

	// Страница выглядит как Z5R, но /stat не ответил: возможно, пароль
	// изменён. Показываем устройство без учётных данных — оператор введёт
	// их сам. Скрывать его нельзя: иначе контроллер просто не найдётся.
	log.Info().Str("ip", ip).
		Msg("найден контроллер Z5R, но пароль не подошёл — нужен ручной ввод")

	return &domain.DiscoveredCamera{
		IP:         ip,
		MAC:        s.getMAC(ip),
		Vendor:     "z5r",
		VendorName: "Z5R WEB BT (IronLogic)",
		HowFound:   "по веб-интерфейсу Z5R-WEB (пароль не подошёл)",
		Online:     true,
	}
}

// looksLikeZ5R проверяет, похожа ли страница устройства на контроллер Z5R.
//
// Проверяем заголовок WWW-Authenticate: контроллер запрашивает
// Basic-авторизацию и указывает в нём название устройства. Проверка
// по заголовку надёжнее разбора тела: на запрос без пароля тело может
// быть пустым или служебным, а заголовок отдаётся всегда.
//
// Точный вид заголовка (измерено на живом контроллере):
//
//	WWW-Authenticate: Basic realm="Z-5R WEB BT 45005541
//
// Обратить внимание на две особенности этой прошивки:
//   - название устройства — **Z-5R**, с дефисом, а не Z5R;
//   - строка не закрыта кавычкой, то есть заголовок обрезан.
//     Поэтому сравнивать надо по вхождению подстроки, а не по целому
//     значению: проверка на равенство никогда не сработает.
func (s *CameraScanner) looksLikeZ5R(ctx context.Context, ip string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ip+"/", nil)
	if err != nil {
		return false
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	auth := resp.Header.Get("WWW-Authenticate")

	// Основной признак: название устройства в области авторизации.
	// Проверяем оба написания — с дефисом (как в прошивке) и без него
	// (как встречается в документации и на других прошивках).
	if strings.Contains(auth, "Z-5R") || strings.Contains(auth, "Z5R") {
		return true
	}
	// Запасной признак: адрес производителя.
	if strings.Contains(auth, "ironlogic") {
		return true
	}

	// На части прошивок авторизация не запрашивается, и заголовка нет.
	// Тогда смотрим заголовок сервера.
	if strings.Contains(strings.ToLower(resp.Header.Get("Server")), "z-5r") {
		return true
	}

	return false
}

// z5rStatInfo — поля /stat, нужные для опознания.
type z5rStatInfo struct {
	SN     string `json:"sn"`
	CtrlFW string `json:"ctrlfw"`
	Mode   string `json:"mode"`
}

// fetchZ5RStat запрашивает /stat с указанными учётными данными.
//
// Проверяем не только код ответа, но и содержимое: признаком успеха
// считаем разобранный серийный номер. Без этого проверка проходила бы
// на любом устройстве, которое отвечает разбираемым JSON-ом.
//
// Отдельная деталь: контроллер отдаёт ответ с Content-Type: text/plain,
// хотя внутри корректный JSON. Поэтому разбираем тело напрямую, а не
// полагаемся на заголовок типа содержимого.
func (s *CameraScanner) fetchZ5RStat(ctx context.Context, ip string, cred credentialPair) (z5rStatInfo, bool) {
	var st z5rStatInfo

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ip+"/stat", nil)
	if err != nil {
		return st, false
	}
	if cred.Username != "" || cred.Password != "" {
		req.SetBasicAuth(cred.Username, cred.Password)
	}
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := s.client.Do(req)
	if err != nil {
		return st, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return st, false
	}

	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return st, false
	}

	// Серийный номер обязателен: он есть у всех контроллеров этой серии
	// и отсутствует у посторонних устройств.
	if st.SN == "" {
		return st, false
	}

	return st, true
}

// credentialHint достаёт подсказку по учётным данным из контекста.
//
// Сканер уже принимает логин и пароль для камер, и логично использовать
// их же для контроллера: оператор вводит одну пару на всю сеть, а не
// отдельно для каждого типа устройства.
func credentialHint(ctx context.Context) (string, string) {
	if c, ok := ctx.Value(credHintKey{}).(credentialPair); ok {
		return c.Username, c.Password
	}
	return "", ""
}

// credHintKey — ключ для передачи подсказки в контексте.
type credHintKey struct{}

// withCredHint кладёт учётные данные в контекст сканирования.
func withCredHint(ctx context.Context, user, pass string) context.Context {
	if user == "" && pass == "" {
		return ctx
	}
	return context.WithValue(ctx, credHintKey{}, credentialPair{user, pass})
}
