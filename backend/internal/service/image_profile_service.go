package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

// Применение профилей изображения с оглядкой на возможности камеры.
//
// Ключевое отличие от простого «применить набор настроек»: профиль —
// это пожелания, а не готовый список ключей. Камеры отличаются сильно,
// и один и тот же ключ есть не у всех. Замерено на живом парке:
//
//	192.168.1.28  22 ключа isp, есть slowShutter
//	192.168.1.48   6 ключей isp, есть exposure, НЕТ slowShutter
//	192.168.1.75   9 ключей isp, есть exposure, НЕТ slowShutter
//
// Поэтому перед применением сверяемся со схемой камеры и говорим
// заранее, что применится, а что нет. Иначе оператор применит профиль,
// решит, что номера будут читаться, а нужного ключа у камеры не окажется.

// ImageProfileService применяет профили и объясняет применимость.
type ImageProfileService struct {
	schemas *SchemaSettingsService
	repo    ImageProfileCameraRepo
	// cameras нужен, чтобы узнать производителя: от него зависит,
	// существуют ли эти настройки на камере вообще.
	cameras CameraRepo
}

// CameraRepo — доступ к камере целиком, а не только к её профилю.
//
// Отдельный интерфейс от ImageProfileCameraRepo, потому что задачи
// разные: один запоминает выбор оператора, второй отвечает на вопрос
// «что это за устройство». Смешивать их значило бы заставлять каждую
// заглушку в тестах реализовывать лишнее.
type CameraRepo interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Camera, error)
}

// ImageProfileCameraRepo запоминает выбранный профиль.
type ImageProfileCameraRepo interface {
	// SetImageProfile сохраняет профиль в настройках камеры.
	//
	// Профиль хранится отдельно от значений: он говорит, ЧТО оператор
	// хотел получить, а не какие числа выставлены сейчас. Без этого
	// нельзя было бы показать, что профиль применён.
	SetImageProfile(ctx context.Context, cameraID uuid.UUID, profileID string) error
	GetImageProfile(ctx context.Context, cameraID uuid.UUID) (string, error)
}

func NewImageProfileService(schemas *SchemaSettingsService, repo ImageProfileCameraRepo) *ImageProfileService {
	return &ImageProfileService{schemas: schemas, repo: repo}
}

// WithCameras подключает источник камер для определения производителя.
//
// Отдельный метод, а не аргумент конструктора: производителя проверяем
// только ради скрытия разделов, и сервис должен уметь работать без этого
// источника — иначе любая заглушка в тестах обязана будет его иметь.
func (s *ImageProfileService) WithCameras(cameras CameraRepo) *ImageProfileService {
	s.cameras = cameras
	return s
}

// ProfileAvailability — что произойдёт, если применить профиль.
type ProfileAvailability struct {
	Profile domain.ImageProfile `json:"profile"`
	// Applicable — сколько полей профиля применится.
	Applicable int `json:"applicable"`
	// Missing — поля, которых нет в схеме этой камеры.
	Missing []string `json:"missing"`
	// Partial — профиль применится не полностью.
	//
	// Главный признак для интерфейса: если true, надо честно сказать
	// оператору, что результата может не быть.
	Partial bool `json:"partial"`
	// Usable — профиль даст нужный результат на этой камере.
	//
	// Считается по RequiredKeys: если ни один из обязательных ключей
	// не поддерживается, профиль бесполезен. Честнее сказать это сразу,
	// чем применить половину настроек и умолчать о второй половине.
	Usable bool `json:"usable"`
	// Changes — что именно изменится: путь и значение.
	Changes []ProfileChange `json:"changes"`
	// UnsupportedReason — почему профиль не даст результата.
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
}

// ProfileChange — одно изменение, которое сделает профиль.
type ProfileChange struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	From  any    `json:"from"`
	To    any    `json:"to"`
}

// Availability считает применимость профиля, не меняя ничего на камере.
//
// Вызывается до нажатия кнопки: оператор должен видеть последствия
// заранее, а не узнавать о них по половине применённых настроек.
func (s *ImageProfileService) Availability(ctx context.Context, cameraID uuid.UUID, profileID string) (*ProfileAvailability, error) {
	profile, ok := domain.FindImageProfile(profileID)
	if !ok {
		return nil, fmt.Errorf("профиль %q не найден", profileID)
	}

	// Проверяем производителя до всего остального, и это не просто
	// вежливая проверка на входе.
	//
	// Профили задают ключи вида `isp.exposure` — они существуют только
	// в прошивке OpenIPC. На камере другого производителя попытка их
	// применить либо не сработает, либо, что хуже, попадёт в чужой
	// обработчик HTTP API и выставит не то. Поэтому на не-OpenIPC
	// не спрашиваем схему вовсе: спрашивать нечего.
	if err := s.requireOpenIPC(ctx, cameraID); err != nil {
		// Отвечаем понятной причиной, а не ошибкой: оператор должен
		// видеть, почему режимы недоступны, и не искать их настройку.
		return &ProfileAvailability{
			Profile:           profile,
			UnsupportedReason: err.Error(),
			Missing:           sortedKeys(profile.Values),
			Partial:           true,
			Usable:            false,
		}, nil
	}

	schema, err := s.schemas.Schema(ctx, cameraID, false)
	if err != nil {
		return nil, err
	}

	view, err := s.schemas.Settings(ctx, cameraID)
	if err != nil {
		return nil, err
	}

	byPath := map[string]SchemaField{}
	for _, sec := range schema.Sections {
		for _, f := range sec.Fields {
			byPath[f.Path] = f
		}
	}

	result := &ProfileAvailability{Profile: profile}

	// Разбираем пожелания в устойчивом порядке: карта Go неупорядочена,
	// а список изменений показывается оператору.
	paths := make([]string, 0, len(profile.Values))
	for path := range profile.Values {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		want := profile.Values[path]

		field, exists := byPath[path]
		if !exists {
			result.Missing = append(result.Missing, path)
			continue
		}

		// Значение null означает «вернуть как было в прошивке».
		// Для такого пожелания ключ нужен, но подставлять значение
		// нечего — камера сама выберет.
		if want == nil {
			result.Applicable++
			result.Changes = append(result.Changes, ProfileChange{
				Path: path, Title: field.Title, From: view.Values[path], To: "по умолчанию",
			})
			continue
		}

		// Проверяем, что камера примет это значение: у неё свои границы
		// и свои допустимые перечисления.
		if err := checkValue(field, want); err != nil {
			// Значение не подходит — считаем поле неприменимым и
			// показываем причину. Записать его всё равно не выйдет.
			result.Missing = append(result.Missing, path)
			continue
		}

		// Изменение показываем только если значение действительно
		// изменится: иначе список пестрел бы строками «то же самое».
		if fmt.Sprint(view.Values[path]) != fmt.Sprint(want) {
			result.Changes = append(result.Changes, ProfileChange{
				Path: path, Title: field.Title, From: view.Values[path], To: want,
			})
		}
		result.Applicable++
	}

	result.Partial = len(result.Missing) > 0

	// Отдельно проверяем обязательные ключи: профиль может применить
	// несколько полей и всё равно не дать результата.
	result.Usable, result.UnsupportedReason = checkRequired(profile, byPath)

	return result, nil
}

// checkRequired проверяет, даст ли профиль результат на этой камере.
//
// Обязательные ключи задаются как список альтернатив: годится любой
// из них. Так выражается то, что выяснилось на живых камерах —
// запретить удлинение выдержки можно двумя разными способами:
//
//	`isp.slowShutter: disabled` — где ключ есть (1.28, 1.41, 1.59)
//	`isp.exposure: 4`           — где его нет, но есть предел числом (1.48, 1.75)
//
// Требовать оба ключа было бы неверно: ни одна камера парка их
// не имеет одновременно.
func checkRequired(profile domain.ImageProfile, byPath map[string]SchemaField) (bool, string) {
	if len(profile.RequiredKeys) == 0 {
		return true, ""
	}

	var present []string
	for _, key := range profile.RequiredKeys {
		if _, ok := byPath[key]; ok {
			present = append(present, key)
		}
	}

	if len(present) > 0 {
		return true, ""
	}

	// Ни одного подходящего ключа — профиль на этой камере бесполезен.
	// Объясняем это словами, а не списком ключей: оператор не обязан
	// знать, что такое `isp.slowShutter`.
	if profile.ID == domain.ProfileOutdoorPlate {
		return false, "на этой камере нет ни запрета длинной выдержки, " +
			"ни ограничения выдержки числом — номера будут смазываться. " +
			"Помочь может обновление прошивки: в сборках Majestic от сентября 2026 " +
			"нужные ключи есть"
	}
	return false, "эта камера не поддерживает нужные настройки: " +
		strings.Join(profile.RequiredKeys, ", ")
}

// Apply применяет профиль.
//
// Записываем только те поля, которые камера поддерживает: остальные
// пропускаем молча, но о них уже сказано заранее в Availability.
func (s *ImageProfileService) Apply(ctx context.Context, cameraID uuid.UUID, profileID string) (*ProfileAvailability, error) {
	availability, err := s.Availability(ctx, cameraID, profileID)
	if err != nil {
		return nil, err
	}

	// На чужой камере применять нечего: ключей прошивки OpenIPC там нет,
	// а запись может попасть в чужой обработчик HTTP API. Останавливаемся
	// здесь, а не полагаемся на то, что камера отвергнет запрос.
	if !availability.Usable {
		reason := availability.UnsupportedReason
		if reason == "" {
			reason = "профиль не поддерживается этой камерой"
		}
		return nil, fmt.Errorf("%s", reason)
	}

	// Собираем только применимые поля: тех, которых нет в схеме,
	// в запросе быть не должно — сервис настроек их отклонит.
	patch := map[string]any{}
	for _, path := range sortedKeys(availability.Profile.Values) {
		if containsString(availability.Missing, path) {
			continue
		}
		value := availability.Profile.Values[path]
		if value == nil {
			// «По умолчанию» записать нельзя: камера сама выберет
			// значение. Оставляем как есть — это не ошибка, а смысл
			// самого пожелания.
			continue
		}
		patch[path] = value
	}

	if len(patch) > 0 {
		if _, err := s.schemas.UpdatePatch(ctx, cameraID, patch); err != nil {
			return nil, err
		}
	}

	// Запоминаем выбранный профиль: он говорит, ЧТО оператор хотел
	// получить, а не какие числа выставлены сейчас.
	if err := s.repo.SetImageProfile(ctx, cameraID, profileID); err != nil {
		// Ошибку не поднимаем: настройки уже применены, и терять их
		// из-за неудачной записи отметки было бы хуже.
		log.Warn().Err(err).Str("camera", cameraID.String()).
			Msg("не удалось сохранить выбранный профиль")
	}

	log.Info().
		Str("camera", cameraID.String()).
		Str("профиль", profileID).
		Int("полей", len(patch)).
		Int("пропущено", len(availability.Missing)).
		Msg("профиль изображения применён")

	return availability, nil
}

// Current отдаёт текущий профиль камеры.
func (s *ImageProfileService) Current(ctx context.Context, cameraID uuid.UUID) (string, error) {
	id, err := s.repo.GetImageProfile(ctx, cameraID)
	if err != nil {
		return "", err
	}
	if id == "" {
		return domain.ProfileCustom, nil
	}
	return id, nil
}

// ListAll собирает применимость всех профилей для камеры.
//
// Нужно интерфейсу: показать список профилей сразу с пометкой, какие
// дадут результат, а какие нет. Считать это по одному запросу на профиль
// было бы wasteful — схема и значения читаются один раз.
func (s *ImageProfileService) ListAll(ctx context.Context, cameraID uuid.UUID) ([]ProfileAvailability, error) {
	// На камере другого производителя профилей нет в принципе: все
	// профили задаются ключами прошивки OpenIPC. Отдаём причину, чтобы
	// оператор понял, почему раздела нет, а не считал это сбоем.
	if err := s.requireOpenIPC(ctx, cameraID); err != nil {
		out := make([]ProfileAvailability, 0, len(domain.ImageProfiles()))
		for _, profile := range domain.ImageProfiles() {
			out = append(out, ProfileAvailability{
				Profile:           profile,
				UnsupportedReason: err.Error(),
				Missing:           sortedKeys(profile.Values),
				Partial:           true,
				Usable:            false,
			})
		}
		return out, nil
	}

	schema, err := s.schemas.Schema(ctx, cameraID, false)
	if err != nil {
		return nil, err
	}
	view, err := s.schemas.Settings(ctx, cameraID)
	if err != nil {
		return nil, err
	}

	byPath := map[string]SchemaField{}
	for _, sec := range schema.Sections {
		for _, f := range sec.Fields {
			byPath[f.Path] = f
		}
	}

	var out []ProfileAvailability
	for _, profile := range domain.ImageProfiles() {
		avail := &ProfileAvailability{Profile: profile}
		for _, path := range sortedKeys(profile.Values) {
			field, exists := byPath[path]
			if !exists {
				avail.Missing = append(avail.Missing, path)
				continue
			}
			want := profile.Values[path]
			if want == nil {
				avail.Applicable++
				avail.Changes = append(avail.Changes, ProfileChange{
					Path: path, Title: field.Title, From: view.Values[path], To: "по умолчанию",
				})
				continue
			}
			if err := checkValue(field, want); err != nil {
				avail.Missing = append(avail.Missing, path)
				continue
			}
			if fmt.Sprint(view.Values[path]) != fmt.Sprint(want) {
				avail.Changes = append(avail.Changes, ProfileChange{
					Path: path, Title: field.Title, From: view.Values[path], To: want,
				})
			}
			avail.Applicable++
		}
		avail.Partial = len(avail.Missing) > 0
		avail.Usable, avail.UnsupportedReason = checkRequired(profile, byPath)
		out = append(out, *avail)
	}

	return out, nil
}

// sortedKeys отдаёт ключи карты в устойчивом порядке.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// requireOpenIPC проверяет, что настройки OpenIPC на этой камере имеют смысл.
//
// Вынесено в отдельный метод, а не сравнение на месте: это одно и то же
// правило для всех разделов, и оно должно меняться в одном месте. Когда
// появится вторая прошивка с таким же доступом, правка будет здесь,
// а не в каждом обработчике по отдельности.
func (s *ImageProfileService) requireOpenIPC(ctx context.Context, cameraID uuid.UUID) error {
	if s.cameras == nil {
		return nil
	}
	cam, err := s.cameras.GetByID(ctx, cameraID)
	if err != nil {
		return fmt.Errorf("не удалось прочитать камеру: %w", err)
	}
	vendor := domain.ResolveVendor(cam)
	if domain.SupportsOpenIPC(vendor) {
		return nil
	}
	// Формулировка важна: оператор читает её на карточке и должен понять
	// не только, что раздела нет, но и почему — иначе он пойдёт искать
	// его в другом месте.
	return fmt.Errorf(
		"режимы съёмки настраиваются только на камерах OpenIPC. Эта камера — %s, у неё другой способ настройки",
		domain.VendorTitle(vendor),
	)
}

// containsString проверяет наличие строки в срезе.
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
