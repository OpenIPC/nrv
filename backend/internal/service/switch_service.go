package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/repository/postgres"
	"github.com/nvr/backend/internal/service/sscpoe"
)

// SwitchService — управление PoE-коммутаторами и мониторинг их портов.
type SwitchService struct {
	repo *postgres.SwitchRepo
	// client — клиент локального протокола. Один на сервис: протокол без
	// сессии, состояние соединения между вызовами не хранится.
	client *sscpoe.Client
	// pollInterval — период опроса. Задан с запасом: коммутаторов на
	// объекте немного, но каждый опрос — это широковещательный пакет, и
	// слишком частая отправка мешала бы остальному трафику в сегменте.
	pollInterval time.Duration
	// powerCycleDelay — пауза между снятием и подачей питания. Значение
	// должно быть достаточным, чтобы устройство успело разрядиться и
	// начать загрузку заново: при слишком короткой паузе камера не
	// заметит пропажу питания и останется в прежнем состоянии.
	powerCycleDelay time.Duration
}

// NewSwitchService создаёт сервис коммутаторов.
func NewSwitchService(repo *postgres.SwitchRepo, iface string, ttl int) *SwitchService {
	return &SwitchService{
		repo:            repo,
		client:          sscpoe.NewClient(iface, ttl),
		pollInterval:    time.Minute,
		powerCycleDelay: 5 * time.Second,
	}
}

// WithIntervals задаёт периоды опроса и паузу цикла питания.
//
// Вынесено отдельно, чтобы значения можно было изменить при отладке, не
// трогая код: подбор паузы приходится делать на живом устройстве.
func (s *SwitchService) WithIntervals(poll, powerCycle time.Duration) *SwitchService {
	if poll > 0 {
		s.pollInterval = poll
	}
	if powerCycle > 0 {
		s.powerCycleDelay = powerCycle
	}
	return s
}

// Search выполняет поиск коммутаторов в сети.
//
// Возвращает найденные устройства, помечая те, что уже добавлены в систему:
// при повторном поиске в списке иначе было бы неясно, добавлять устройство
// или оно уже есть.
func (s *SwitchService) Search(ctx context.Context) ([]SwitchFound, error) {
	found := s.client.Search(ctx)

	out := make([]SwitchFound, 0, len(found))
	for _, d := range found {
		item := SwitchFound{
			SN:    d.SN,
			IP:    d.IP,
			MAC:   strings.ToUpper(d.MAC),
			Model: d.Model,
		}
		if item.Model == "" {
			item.Model = sscpoe.ModelFromSN(d.SN)
		}

		existing, err := s.repo.GetSwitchBySN(ctx, d.SN)
		if err == nil && existing != nil {
			item.Added = true
			item.SwitchID = &existing.ID
			item.Name = existing.Name
			// Адрес мог смениться по DHCP: показываем текущий, а не
			// сохранённый, иначе оператор пойдёт по устаревшему адресу.
			item.IP = d.IP
		} else if err != nil && !errors.Is(err, postgres.ErrSwitchNotFound) {
			// Ошибка чтения не должна ломать поиск целиком: устройство
			// найдено, и это главное. Пометку «уже добавлено» получит
			// только то, что удалось прочитать.
			log.Warn().Err(err).Str("sn", d.SN).Msg("не удалось проверить наличие коммутатора")
		}
		out = append(out, item)
	}
	return out, nil
}

// SwitchFound — результат поиска коммутатора.
type SwitchFound struct {
	SN       string     `json:"sn"`
	IP       string     `json:"ip"`
	MAC      string     `json:"mac"`
	Model    string     `json:"model"`
	Name     string     `json:"name"`
	Added    bool       `json:"added"`
	SwitchID *uuid.UUID `json:"switch_id,omitempty"`
}

// Add добавляет коммутатор по серийному номеру.
//
// Первый опрос выполняется тут же: добавлять устройство «на веру» нельзя —
// если связи с ним нет, оператор получил бы пустую карточку и не понял,
// в чём дело. Ошибка опроса не отменяет добавление: устройство может быть
// временно недоступно, а данные о нём (имя, расположение) уже осмысленны,
// и стирать их пришлось бы вводить заново.
func (s *SwitchService) Add(ctx context.Context, sn, name, location, password string) (*domain.Switch, error) {
	sn = strings.TrimSpace(sn)
	if sn == "" {
		return nil, errors.New("не указан серийный номер коммутатора")
	}

	if _, err := s.repo.GetSwitchBySN(ctx, sn); err == nil {
		return nil, postgres.ErrSwitchExists
	} else if !errors.Is(err, postgres.ErrSwitchNotFound) {
		return nil, err
	}

	if name == "" {
		name = sn
	}

	sw := &domain.Switch{
		SN:       sn,
		Name:     name,
		Location: location,
		Password: password,
		// Порядок нумерации определяется по серийному номеру и сохраняется
		// в базу: правило выведено по моделям парка, и на новой модели оно
		// может не сработать. Сохранённое значение можно исправить, а
		// вычисляемое на каждом опросе — нет.
		PortsReversed: sscpoe.PortsReversedFromSN(sn),
	}

	id, err := s.repo.UpsertSwitch(ctx, sw)
	if err != nil {
		return nil, err
	}

	if err := s.PollByID(ctx, id); err != nil {
		log.Warn().Err(err).Str("sn", sn).Msg("первый опрос коммутатора не удался")
	}

	return s.repo.GetSwitch(ctx, id)
}

// List возвращает коммутаторы.
func (s *SwitchService) List(ctx context.Context) ([]domain.Switch, error) {
	return s.repo.ListSwitches(ctx)
}

// Get возвращает коммутатор с портами.
func (s *SwitchService) Get(ctx context.Context, id uuid.UUID) (*domain.Switch, error) {
	return s.repo.GetSwitch(ctx, id)
}

// UpdateMeta сохраняет имя, расположение и порядок нумерации.
func (s *SwitchService) UpdateMeta(ctx context.Context, id uuid.UUID, name, location string, portsReversed bool) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("название коммутатора не может быть пустым")
	}
	return s.repo.UpdateSwitchMeta(ctx, id, name, location, portsReversed)
}

// Delete удаляет коммутатор.
//
// Отказ при наличии привязанных камер: удаление снимает и привязки, а
// тогда оператор потеряет сведения о том, где эти камеры были подключены,
// и при следующем сбое не найдёт нужный порт. Пусть сначала снимет
// привязки осознанно.
func (s *SwitchService) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := s.repo.CountCameras(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("к коммутатору привязано камер: %d. Снимите привязки перед удалением", n)
	}
	return s.repo.DeleteSwitch(ctx, id)
}

// PollAll опрашивает все известные коммутаторы.
func (s *SwitchService) PollAll(ctx context.Context) {
	list, err := s.repo.ListOnlineSwitches(ctx)
	if err != nil {
		log.Error().Err(err).Msg("не удалось получить список коммутаторов")
		return
	}
	for _, sw := range list {
		if err := s.PollByID(ctx, sw.ID); err != nil {
			log.Warn().Err(err).Str("sn", sw.SN).Msg("опрос коммутатора не удался")
		}
	}
}

// PollByID опрашивает один коммутатор и сохраняет состояние портов.
func (s *SwitchService) PollByID(ctx context.Context, id uuid.UUID) error {
	sw, err := s.repo.GetSwitch(ctx, id)
	if err != nil {
		return err
	}

	det, err := s.client.DetailWithPassword(ctx, sw.SN, sw.Password)
	now := time.Now()

	if err != nil {
		// Отмечаем недоступность и сохраняем текст ошибки: оператору нужно
		// отличать таймаут от отказа устройства, иначе «офлайн» ничего не
		// объясняет и заставляет проверять всё подряд.
		sw.Online = false
		sw.LastError = describeSwitchError(err)
		if _, uerr := s.repo.UpsertSwitch(ctx, sw); uerr != nil {
			return uerr
		}
		return err
	}

	sw.Online = true
	sw.LastError = ""
	sw.LastSeenAt = &now
	if det.IP != "" {
		sw.IP = det.IP
	}
	if det.MAC != "" {
		sw.MAC = strings.ToUpper(det.MAC)
	}
	if det.V != "" {
		sw.Firmware = det.V
	}
	// Модель в ответе на запрос состояния не приходит — только в ответе на
	// поиск, а его к этому моменту уже нет. Восстанавливаем из серийного
	// номера: он содержит обозначение модели, и это лучше пустого поля,
	// по которому нельзя отличить один коммутатор от другого.
	if sw.Model == "" {
		sw.Model = sscpoe.ModelFromSN(sw.SN)
	}
	sw.Voltage = sscpoe.ParseNumber(det.Vol)
	// Температуру берём через разбор по правдоподобности: единого поля
	// нет, у одной модели это tp, у другой T, причём у второй в tp лежит
	// мусор.
	sw.Temperature = det.Temperature()
	sw.PortCount = det.PortCount()

	// Полный ответ сохраняем целиком. Набор полей различается от модели к
	// модели, и жёсткая схема ломалась бы на новом устройстве; здесь же
	// остаётся всё, что сообщил коммутатор, и разбор можно уточнить позже,
	// не перезапрашивая устройство.
	if raw, err := json.Marshal(det); err == nil {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			sw.Detail = m
		}
	}

	ports := s.buildPorts(sw, det)

	if _, err := s.repo.UpsertSwitch(ctx, sw); err != nil {
		return err
	}
	if err := s.repo.SavePorts(ctx, sw.ID, ports); err != nil {
		return err
	}
	return nil
}

// buildPorts превращает ответ устройства в список портов.
//
// Здесь решаются две тонкости протокола, каждая из которых при ошибке
// приводит к управлению не тем портом.
//
// Первая: индекс массива в ответе и номер порта на корпусе — не одно и то
// же. У моделей с обратной нумерацией физический порт 1 соответствует
// последнему элементу ответа.
//
// Вторая: массивы разной длины. Число портов берётся из массива линков,
// а питание и потребление есть только у PoE-портов, и их может быть
// меньше общего числа. На GPS204V3 портов шесть, а PoE — на четырёх:
// пятый порт занят модемом, и попытка выключить на нём питание ушла бы
// в никуда. Поэтому PoE-поля заполняются только для индексов, реально
// существующих в массиве питания.
func (s *SwitchService) buildPorts(sw *domain.Switch, det *sscpoe.Detail) []domain.SwitchPort {
	count := det.PortCount()
	poeCount := det.PoECount()
	ports := make([]domain.SwitchPort, 0, count)

	for i := 0; i < count; i++ {
		// Номер порта, который видит оператор.
		portNumber := i + 1
		if sw.PortsReversed {
			portNumber = count - i
		}

		p := domain.SwitchPort{
			SwitchID:   sw.ID,
			PortNumber: portNumber,
		}

		if i < len(det.Link) {
			// Ненулевое значение означает наличие связи. Опираться на
			// конкретное значение 4 нельзя: у разных моделей и скоростей
			// оно иное.
			p.LinkUp = det.Link[i] != 0
		}
		if i < len(det.PhyC) {
			p.SpeedMbps = domain.SpeedMbpsValue(det.PhyC[i])
		}

		// Порт умеет питать, если он попал в массив питания. Транзитный
		// порт в этот массив не входит, и команды питания на нём
		// запрещаются — иначе можно было бы отключить сам uplink.
		if i < poeCount {
			p.PoeCapable = true
			p.PoeEnabled = det.PoeC[i] != 0
			if i < len(det.PW) {
				p.PoeWatts = sscpoe.ParseNumber(det.PW[i])
			}
		}
		// Транзитный порт помечаем отдельно: он может быть и PoE-портом,
		// но выключать его нельзя — через него идёт весь трафик.
		if det.IsUplink(i) {
			p.IsUplink = true
		}

		if i < len(det.Tx) {
			p.TxMB = sscpoe.ParseNumber(det.Tx[i])
		}
		if i < len(det.Rx) {
			p.RxMB = sscpoe.ParseNumber(det.Rx[i])
		}
		if det.Isoc != nil && *det.Isoc != 0 {
			p.Isolated = true
		}

		ports = append(ports, p)
	}
	return ports
}

// RunPolling запускает периодический опрос до отмены контекста.
func (s *SwitchService) RunPolling(ctx context.Context) {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	// Первый опрос сразу, не дожидаясь периода: после запуска сервера
	// страница коммутаторов должна показывать состояние, а не пустоту в
	// течение целой минуты.
	s.PollAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.PollAll(ctx)
		}
	}
}

// ExecutePortAction выполняет действие над портом.
//
// Порядок шагов важен: сначала проверяем применимость действия, потом
// выполняем, и только затем пишем в журнал. Запись «выполнено» до отправки
// команды создавала бы ложную картину при обрыве связи между проверкой и
// отправкой.
func (s *SwitchService) ExecutePortAction(ctx context.Context, switchID uuid.UUID, portNumber int, action domain.PortAction, actor string) error {
	if !action.IsValid() {
		return fmt.Errorf("неизвестное действие: %q", action)
	}

	sw, err := s.repo.GetSwitch(ctx, switchID)
	if err != nil {
		return err
	}

	port, err := s.repo.GetPort(ctx, switchID, portNumber)
	if err != nil {
		return err
	}

	// Проверка до отправки команды. Действия питания на порту, который не
	// умеет питать, отправлять нельзя: смысла в них нет, а ответ
	// устройства может оказаться неожиданным.
	if isPowerAction(action) && !port.PoeCapable {
		return fmt.Errorf("порт %d не поддерживает питание PoE", portNumber)
	}
	// Отдельный запрет для транзитного порта. Через него идёт канал связи
	// с сервером: сняв на нём питание, мы потеряем управление всем
	// коммутатором и не сможем подать питание обратно.
	if isPowerAction(action) && port.IsUplink {
		return fmt.Errorf("порт %d — транзитный: его отключение лишит связи весь коммутатор", portNumber)
	}

	// Вход выполняем перед командой: без него устройство не примет
	// управляющий запрос, и команда уйдёт в пустоту. Модели, не требующие
	// пароля, на это никак не отреагируют.
	if s.needsLogin(sw) {
		if sw.Password == "" {
			return sscpoe.ErrAuthRequired
		}
		if err := s.client.Login(ctx, sw.SN, sw.Password); err != nil {
			return err
		}
	}

	index, err := sscpoe.PortIndex(portNumber, sw.PortCount, sw.PortsReversed)
	if err != nil {
		return err
	}

	// Перезагрузка питания выполняется как два действия с паузой, а не
	// одной командой: устройства поддерживают отдельные команды включения
	// и выключения, а составной команды у них нет.
	if action == domain.PortActionPowerCycle {
		return s.powerCycle(ctx, sw, port, index, actor)
	}

	opcode, err := sscpoe.OpCode(action, index)
	if err != nil {
		return err
	}

	err = s.applyOpcode(ctx, sw.SN, action, opcode)
	result := "ok"
	message := ""
	if err != nil {
		result = "error"
		message = err.Error()
	}

	// Журналируем в любом случае — и успех, и неудачу: при разборе
	// инцидента важно знать не только что команда сработала, но и что её
	// пытались выполнить, и почему не вышло.
	if lerr := s.repo.LogEvent(ctx, &domain.SwitchPortEvent{
		SwitchID:   sw.ID,
		SwitchSN:   sw.SN,
		PortNumber: portNumber,
		Action:     action,
		Result:     result,
		Message:    message,
		Actor:      actor,
	}); lerr != nil {
		log.Warn().Err(lerr).Msg("не удалось записать действие в журнал")
	}

	if err != nil {
		return err
	}

	// Обновляем состояние порта сразу, не дожидаясь следующего опроса:
	// иначе оператор в течение минуты видел бы старое состояние и решил,
	// что команда не сработала.
	poeEnabled := port.PoeEnabled
	extendMode := port.ExtendMode
	switch action {
	case domain.PortActionPowerOn:
		poeEnabled = true
	case domain.PortActionPowerOff:
		poeEnabled = false
	case domain.PortActionExtendOn:
		extendMode = true
	case domain.PortActionExtendOff:
		extendMode = false
	}
	if err := s.repo.SetPortState(ctx, switchID, portNumber, poeEnabled, extendMode); err != nil {
		log.Warn().Err(err).Msg("не удалось обновить состояние порта")
	}

	// После смены состояния полезно перечитать устройство: команда
	// принята, но фактическое состояние может отличаться — например,
	// линк поднимается не мгновенно.
	go func(id uuid.UUID) {
		pollCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.PollByID(pollCtx, id); err != nil {
			log.Debug().Err(err).Msg("опрос после команды не удался")
		}
	}(switchID)

	return nil
}

// powerCycle снимает и снова подаёт питание на порт.
func (s *SwitchService) powerCycle(ctx context.Context, sw *domain.Switch, port *domain.SwitchPort, index int, actor string) error {
	offCode, err := sscpoe.OpCode(domain.PortActionPowerOff, index)
	if err != nil {
		return err
	}
	onCode, err := sscpoe.OpCode(domain.PortActionPowerOn, index)
	if err != nil {
		return err
	}

	if err := s.applyOpcode(ctx, sw.SN, domain.PortActionPowerOff, offCode); err != nil {
		s.logEvent(ctx, sw, port.PortNumber, domain.PortActionPowerCycle, "error",
			"не удалось снять питание: "+err.Error(), actor)
		return fmt.Errorf("не удалось снять питание: %w", err)
	}

	// Обновляем состояние сразу после снятия питания: если подача не
	// удастся, интерфейс и база всё равно будут знать, что порт обесточен.
	// Иначе камера осталась бы без питания, а система считала бы её
	// рабочей — и оператор искал бы причину в самой камере.
	if err := s.repo.SetPortState(ctx, sw.ID, port.PortNumber, false, port.ExtendMode); err != nil {
		log.Warn().Err(err).Msg("не удалось сохранить состояние порта после снятия питания")
	}

	select {
	case <-ctx.Done():
		// Контекст отменён: питание осталось снятым. Подаём его в новом
		// контексте, потому что оставлять камеру обесточенной нельзя —
		// это хуже, чем продолжить работу после отмены запроса.
		bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.applyOpcode(bg, sw.SN, domain.PortActionPowerOn, onCode); err == nil {
			_ = s.repo.SetPortState(bg, sw.ID, port.PortNumber, true, port.ExtendMode)
		}
		s.logEvent(ctx, sw, port.PortNumber, domain.PortActionPowerCycle, "error",
			"отменено, питание восстановлено", actor)
		return ctx.Err()
	case <-time.After(s.powerCycleDelay):
	}

	if err := s.applyOpcode(ctx, sw.SN, domain.PortActionPowerOn, onCode); err != nil {
		// Это самый опасный исход: питание снято и не подано. Пишем в
		// журнал отдельным текстом, чтобы при разборе сразу было видно,
		// почему камера пропала.
		s.logEvent(ctx, sw, port.PortNumber, domain.PortActionPowerCycle, "error",
			"питание снято и не восстановлено: "+err.Error(), actor)
		return fmt.Errorf("питание снято, но обратно не подано: %w", err)
	}

	if err := s.repo.SetPortState(ctx, sw.ID, port.PortNumber, true, port.ExtendMode); err != nil {
		log.Warn().Err(err).Msg("не удалось сохранить состояние порта")
	}
	if err := s.repo.MarkPowerCycle(ctx, sw.ID, port.PortNumber); err != nil {
		log.Warn().Err(err).Msg("не удалось отметить перезагрузку питания")
	}

	s.logEvent(ctx, sw, port.PortNumber, domain.PortActionPowerCycle, "ok", "", actor)

	go func(id uuid.UUID) {
		// Пауза перед перечитыванием: устройству нужно время поднять
		// питание и поднять линк, и сразу после команды состояние ещё
		// старое.
		time.Sleep(3 * time.Second)
		pollCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.PollByID(pollCtx, id); err != nil {
			log.Debug().Err(err).Msg("опрос после перезагрузки питания не удался")
		}
	}(sw.ID)

	return nil
}

// applyOpcode отправляет команду с запасным кодом при необходимости.
//
// Повтор допускается только при коде «операция не понята»: при любой другой
// ошибке команда могла выполниться, но ответ не дойти, и повтор
// переключил бы порт второй раз.
func (s *SwitchService) applyOpcode(ctx context.Context, sn string, action domain.PortAction, opcode int) error {
	err := s.client.Config(ctx, sn, opcode)
	if err == nil {
		return nil
	}
	if !sscpoe.IsFallbackCode(err) {
		return err
	}

	alt, ok := sscpoe.FallbackOpCode(action)
	if !ok {
		return err
	}
	log.Info().Str("sn", sn).Int("opcode", opcode).Str("action", string(action)).
		Msg("модель не поняла код, пробуем запасной")
	return s.client.Config(ctx, sn, alt)
}

// logEvent пишет запись в журнал, не прерывая работу при ошибке.
func (s *SwitchService) logEvent(ctx context.Context, sw *domain.Switch, port int, action domain.PortAction, result, message, actor string) {
	if err := s.repo.LogEvent(ctx, &domain.SwitchPortEvent{
		SwitchID:   sw.ID,
		SwitchSN:   sw.SN,
		PortNumber: port,
		Action:     action,
		Result:     result,
		Message:    message,
		Actor:      actor,
	}); err != nil {
		log.Warn().Err(err).Msg("не удалось записать событие порта в журнал")
	}
}

// isPowerAction сообщает, что действие управляет питанием.
func isPowerAction(a domain.PortAction) bool {
	switch a {
	case domain.PortActionPowerOn, domain.PortActionPowerOff, domain.PortActionPowerCycle:
		return true
	}
	return false
}

// BindCamera привязывает камеру к порту.
func (s *SwitchService) BindCamera(ctx context.Context, cameraID, switchID uuid.UUID, portNumber int) error {
	return s.repo.BindCamera(ctx, cameraID, switchID, portNumber)
}

// UnbindCamera снимает привязку камеры к порту.
func (s *SwitchService) UnbindCamera(ctx context.Context, cameraID uuid.UUID) error {
	return s.repo.UnbindCamera(ctx, cameraID)
}

// CameraLink возвращает подключение камеры.
func (s *SwitchService) CameraLink(ctx context.Context, cameraID uuid.UUID) (*postgres.CameraPortLink, error) {
	return s.repo.CameraLink(ctx, cameraID)
}

// CameraLinks возвращает подключения набора камер.
func (s *SwitchService) CameraLinks(ctx context.Context, cameraIDs []uuid.UUID) (map[uuid.UUID]*postgres.CameraPortLink, error) {
	return s.repo.CameraLinks(ctx, cameraIDs)
}

// Events возвращает журнал действий.
func (s *SwitchService) Events(ctx context.Context, switchID *uuid.UUID, limit int) ([]domain.SwitchPortEvent, error) {
	return s.repo.ListEvents(ctx, switchID, limit)
}

// PollNow запускает внеочередной опрос одного коммутатора.
func (s *SwitchService) PollNow(ctx context.Context, id uuid.UUID) error {
	return s.PollByID(ctx, id)
}

// SetPassword сохраняет пароль коммутатора и сразу проверяет его.
//
// Проверка при сохранении обязательна: без неё оператор мог бы записать
// ошибочный пароль и узнать об этом только при первом сбое камеры — в
// самый неподходящий момент. Пустой пароль допустим и означает «у этой
// модели вход не нужен».
func (s *SwitchService) SetPassword(ctx context.Context, id uuid.UUID, password string) error {
	sw, err := s.repo.GetSwitch(ctx, id)
	if err != nil {
		return err
	}

	if password != "" {
		if err := s.client.Login(ctx, sw.SN, password); err != nil {
			return fmt.Errorf("пароль не принят коммутатором: %w", err)
		}
	}

	if err := s.repo.SetPassword(ctx, id, password); err != nil {
		return err
	}

	// Сразу перечитываем состояние: до сохранения пароля закрытый
	// коммутатор числился недоступным, и без опроса карточка осталась бы
	// с ошибкой, хотя связь уже есть.
	return s.PollByID(ctx, id)
}

// needsLogin сообщает, требуется ли вход этому коммутатору.
//
// Признак — сохранённый пароль: только оператор знает, закрыта модель или
// нет, а пытаться входить без пароля на открытой модели означало бы
// тратить время на заведомо неудачный запрос при каждом действии.
func (s *SwitchService) needsLogin(sw *domain.Switch) bool {
	return sw.Password != ""
}

// describeSwitchError переводит ошибку протокола в текст для оператора.
//
// Общий текст «офлайн» бесполезен: за ним скрываются разные причины с
// разными действиями. Таймаут означает, что коммутатор выключен или не
// доступен по сети, отказ устройства — что оно на связи, но команду не
// принимает, а требование пароля — что устройство работает, но закрыто.
func describeSwitchError(err error) string {
	switch {
	case errors.Is(err, sscpoe.ErrTimeout):
		return "нет ответа: коммутатор выключен или недоступен по сети"
	case errors.Is(err, sscpoe.ErrNoRoute):
		return "нет маршрута до коммутатора: проверьте адрес и подсеть"
	case errors.Is(err, sscpoe.ErrWrongPassword):
		return "коммутатор отклонил пароль: проверьте его в настройках"
	case errors.Is(err, sscpoe.ErrAuthRequired):
		return "коммутатор требует пароль: задайте его в настройках"
	case errors.Is(err, sscpoe.ErrDeviceCode):
		return "устройство вернуло ошибку: " + err.Error()
	default:
		return err.Error()
	}
}

// parseFloat разбирает число из ответа устройства.
//
// Устройство присылает часть значений строками, и пустая строка — обычное
// дело. Некорректное значение даёт 0, а не ошибку: отсутствие одного числа
// не должно ломать показ всего состояния порта.
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
