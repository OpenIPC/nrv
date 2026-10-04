package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	"github.com/nvr/backend/internal/service/acs"
	"github.com/rs/zerolog/log"
)

// Обновление прошивки контроллеров СКУД по OTA.
//
// Сервер выступает посредником: образ прошивки лежит на нём, а контроллер
// получает его по своему эндпоинту /api/ota. Оператору не нужно
// подключаться к устройству напрямую — обновление запускается из интерфейса.
//
// Обновление нельзя выполнить одним запросом: контроллер перезагружается и
// какое-то время не отвечает. Поэтому процесс разбит на шаги, и его ход
// виден в журнале — иначе оператор не поймёт, зависло устройство или
// прошивается.

// firmwareDir — каталог с образами прошивок на сервере.
const firmwareDir = "/var/lib/nvr/firmware"

// FirmwareImage описывает образ прошивки, доступный для установки.
type FirmwareImage struct {
	// Name — имя файла, по нему образ выбирается при обновлении.
	Name string `json:"name"`
	// Version — версия из имени файла или пустая, если её не удалось
	// определить. Имя вида skud-1.0.0.bin разбирается на версию.
	Version string `json:"version"`
	Size    int64  `json:"size"`
	// SHA256 — контрольная сумма: по ней видно, что образ не повреждён,
	// и что на разные контроллеры ушёл один и тот же файл.
	SHA256 string `json:"sha256"`
	// UploadedAt — время появления файла на сервере.
	UploadedAt time.Time `json:"uploaded_at"`
}

// OTAUpdate — состояние обновления конкретного контроллера.
type OTAUpdate struct {
	ControllerID uuid.UUID `json:"controller_id"`
	// State: running — идёт, done — успешно, failed — ошибка.
	State string `json:"state"`
	// Step — текущий шаг: upload, reboot, verify.
	Step string `json:"step"`
	// Message — код хода обновления (ota.uploading и т. п.) либо готовый
	// текст ошибки от устройства.
	//
	// Код, а не готовая фраза: интерфейс показывается на четырёх языках, и
	// русский текст, записанный здесь, показался бы одинаковым всем.
	// Подпись к коду ставит интерфейс (см. webui/src/components/FirmwareModal.tsx).
	Message string `json:"message"`
	// FromVersion и ToVersion — версии до и после обновления.
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version,omitempty"`
	// ExpectedVersion — версия из имени файла. Отдаётся отдельно, потому
	// что она может не совпасть с ToVersion: тогда оператору нужно видеть
	// обе, а не одну.
	ExpectedVersion string    `json:"expected_version,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
}

// FirmwareService хранит образы прошивок и запускает обновления.
type FirmwareService struct {
	acsSvc *ACSService

	mu      sync.Mutex
	updates map[uuid.UUID]*OTAUpdate
}

func NewFirmwareService(acsSvc *ACSService) *FirmwareService {
	if err := os.MkdirAll(firmwareDir, 0o755); err != nil {
		log.Warn().Err(err).Str("каталог", firmwareDir).
			Msg("не удалось создать каталог прошивок")
	}
	return &FirmwareService{
		acsSvc:  acsSvc,
		updates: make(map[uuid.UUID]*OTAUpdate),
	}
}

// versionFromName извлекает версию из имени файла.
//
// Поддерживается вид skud-1.0.0.bin: версию удобно видеть прямо в списке,
// не открывая файл. Если имя не подходит, версия остаётся пустой —
// это не мешает прошить, но подсказку о ней интерфейс не покажет.
func versionFromName(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))

	// Версия считается частью после последнего дефиса или подчёркивания,
	// если она состоит из цифр и точек.
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '-' || base[i] == '_' {
			candidate := base[i+1:]
			if isVersionLike(candidate) {
				return candidate
			}
			return ""
		}
	}

	if isVersionLike(base) {
		return base
	}
	return ""
}

// isVersionLike проверяет, похожа ли строка на версию: только цифры и точки.
func isVersionLike(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '.':
		default:
			return false
		}
	}
	return hasDigit
}

// ListFirmwares возвращает образы, лежащие на сервере.
//
// Читаются из каталога, а не из базы: образ — это файл, и источником
// истины должен быть он. Иначе при ручном добавлении файла список
// разошёлся бы с содержимым каталога.
func (s *FirmwareService) ListFirmwares() ([]FirmwareImage, error) {
	entries, err := os.ReadDir(firmwareDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []FirmwareImage{}, nil
		}
		return nil, err
	}

	images := make([]FirmwareImage, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}

		full := filepath.Join(firmwareDir, e.Name())
		sum, err := fileSHA256(full)
		if err != nil {
			log.Warn().Err(err).Str("файл", e.Name()).
				Msg("не удалось посчитать сумму прошивки")
		}

		images = append(images, FirmwareImage{
			Name:       e.Name(),
			Version:    versionFromName(e.Name()),
			Size:       info.Size(),
			SHA256:     sum,
			UploadedAt: info.ModTime(),
		})
	}

	// Свежие образы сверху: обычно нужен последний.
	sort.Slice(images, func(i, j int) bool {
		return images[i].UploadedAt.After(images[j].UploadedAt)
	})
	return images, nil
}

// SaveFirmware сохраняет загруженный образ на сервер.
//
// Имя файла — это версия, по ней образ выбирается в интерфейсе, поэтому имя
// проверяется: без него выбрать нужную прошивку будет невозможно.
func (s *FirmwareService) SaveFirmware(name string, data []byte) (*FirmwareImage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("файл прошивки пуст")
	}

	// Оставляем только имя файла: имя с путём позволило бы записать файл
	// за пределы каталога прошивок.
	name = filepath.Base(name)
	if !strings.HasSuffix(name, ".bin") {
		return nil, fmt.Errorf("прошивка должна быть файлом .bin")
	}
	if strings.ContainsAny(name, "/\\") {
		return nil, fmt.Errorf("недопустимое имя файла")
	}

	// Образ должен начинаться с заголовка приложения ESP-IDF (0xE9).
	// Проверка отсекает случайную загрузку: прошивка не того чипа или
	// вообще не файл привела бы контроллер в нерабочее состояние.
	if len(data) < 4 || data[0] != 0xE9 {
		return nil, fmt.Errorf("файл не похож на прошивку ESP32 (нет заголовка)")
	}

	// Размер образа не может превышать OTA-раздел: контроллер отклонит
	// слишком большой файл уже в процессе записи, потратив время.
	const maxFirmwareSize = 3 * 1024 * 1024
	if len(data) > maxFirmwareSize {
		return nil, fmt.Errorf("прошивка больше %d КБ, раздел OTA её не вместит",
			maxFirmwareSize/1024)
	}

	full := filepath.Join(firmwareDir, name)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return nil, fmt.Errorf("сохранить прошивку: %w", err)
	}

	sum := sha256.Sum256(data)
	info, _ := os.Stat(full)

	log.Info().Str("файл", name).Int("размер", len(data)).
		Str("версия", versionFromName(name)).Msg("прошивка загружена на сервер")

	img := &FirmwareImage{
		Name:    name,
		Version: versionFromName(name),
		Size:    int64(len(data)),
		SHA256:  hex.EncodeToString(sum[:]),
	}
	if info != nil {
		img.UploadedAt = info.ModTime()
	}
	return img, nil
}

// DeleteFirmware удаляет образ с сервера.
func (s *FirmwareService) DeleteFirmware(name string) error {
	name = filepath.Base(name)
	if !strings.HasSuffix(name, ".bin") {
		return fmt.Errorf("неверное имя файла")
	}
	full := filepath.Join(firmwareDir, name)
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("прошивка не найдена")
		}
		return err
	}
	return nil
}

// GetUpdate возвращает состояние обновления контроллера.
func (s *FirmwareService) GetUpdate(controllerID uuid.UUID) *OTAUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.updates[controllerID]; ok {
		copied := *u
		return &copied
	}
	return nil
}

// setUpdate сохраняет состояние обновления.
func (s *FirmwareService) setUpdate(u *OTAUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates[u.ControllerID] = u
}

// StartUpdate запускает обновление прошивки контроллера.
//
// Работает в фоне: загрузка мегабайта на контроллер и последующая
// перезагрузка занимают минуты, и держать на этом HTTP-запрос нельзя.
// Ход обновления виден через GetUpdate.
func (s *FirmwareService) StartUpdate(ctx context.Context, controllerID uuid.UUID, firmwareName string) (*OTAUpdate, error) {
	s.mu.Lock()
	if u, ok := s.updates[controllerID]; ok && u.State == "running" {
		s.mu.Unlock()
		return nil, fmt.Errorf("обновление этого контроллера уже выполняется")
	}
	s.mu.Unlock()

	name := filepath.Base(firmwareName)
	path := filepath.Join(firmwareDir, name)
	image, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("прошивка не найдена на сервере: %w", err)
	}

	ctrl, err := s.acsSvc.GetController(ctx, controllerID)
	if err != nil {
		return nil, fmt.Errorf("контроллер не найден: %w", err)
	}

	adapter, err := s.acsSvc.firmwareAdapter(ctx, ctrl)
	if err != nil {
		return nil, err
	}

	update := &OTAUpdate{
		ControllerID: controllerID,
		State:        "running",
		Step:         "upload",
		Message:      "ota.uploading",
		StartedAt:    time.Now(),
	}
	s.setUpdate(update)

	go s.runUpdate(adapter, ctrl, image, name, update)

	return update, nil
}

// runUpdate выполняет обновление и ведёт журнал его шагов.
func (s *FirmwareService) runUpdate(adapter acs.FirmwareManager, ctrl *domain.ACSController,
	image []byte, name string, update *OTAUpdate) {

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Версию до обновления фиксируем, чтобы потом показать, что изменилось.
	// Ошибку игнорируем: у старых прошивок версии может не быть.
	if info, err := adapter.GetFirmwareInfo(ctx); err == nil {
		update.FromVersion = info.Version
	}

	log.Info().Str("контроллер", ctrl.Name).Str("файл", name).
		Int("размер", len(image)).Msg("OTA: начинаю обновление")

	uploadErr := adapter.UploadFirmware(ctx, image)

	// Обрыв соединения при загрузке не означает провал: контроллер мог
	// успеть записать образ и уйти в перезагрузку, не отдав ответ. Поэтому
	// окончательный вердикт даётся по версии прошивки после старта —
	// именно она показывает, применилось ли обновление. Ошибку сохраняем,
	// чтобы показать её, если версия не изменится.
	if uploadErr != nil {
		log.Warn().Err(uploadErr).Str("контроллер", ctrl.Name).
			Msg("OTA: загрузка завершилась с ошибкой, проверяю версию после перезагрузки")
	}

	// После успешной записи контроллер перезагружается сам.
	update.Step = "reboot"
	update.Message = "ota.rebooting"
	s.setUpdate(update)
	log.Info().Str("контроллер", ctrl.Name).Msg("OTA: образ отправлен, ждём перезагрузки")

	update.Step = "verify"
	update.Message = "ota.verifying"
	s.setUpdate(update)

	version, err := adapter.VerifyFirmwareVersion(ctx, 3*time.Minute)
	if err != nil {
		if uploadErr != nil {
			s.fail(update, "upload", fmt.Errorf("передать прошивку: %w", uploadErr))
			return
		}
		s.fail(update, "verify", fmt.Errorf("контроллер не вышел на связь: %w", err))
		return
	}

	update.ToVersion = version
	update.FinishedAt = time.Now()

	expected := versionFromName(name)
	switch {
	case version == "":
		// Контроллер не отдаёт версию. Это означает, что обновление не
		// применилось: прошивка с версией её всегда сообщает. Считать
		// такой результат успешным нельзя — оператор решил бы, что
		// устройство обновлено, и не стал бы разбираться.
		s.fail(update, "verify", fmt.Errorf(
			"контроллер не сообщает версию прошивки — обновление не применилось "+
				"(на устройстве осталась прошивка без поддержки версий)"))
		return
	case version == update.FromVersion && update.FromVersion != "":
		// Версия не изменилась — прошивка не применилась.
		s.fail(update, "verify", fmt.Errorf(
			"контроллер остался на версии %s — прошивка не применилась", version))
		return
	case expected != "" && expected != version:
		update.State = "done"
		update.Step = "done"
		// Версии расходятся — это отдельный случай: обновление прошло,
		// но применилось не то, что оператор выбрал. Обе версии отдаём
		// в поля, чтобы интерфейс показал их на своём языке.
		update.Message = "ota.version_mismatch"
		update.ExpectedVersion = expected
		log.Warn().Str("контроллер", ctrl.Name).
			Str("ожидалась", expected).Str("получена", version).
			Msg("OTA: версия после обновления не совпала с именем файла")
	default:
		update.State = "done"
		update.Step = "done"
		update.Message = "ota.done"
	}
	s.setUpdate(update)

	log.Info().Str("контроллер", ctrl.Name).Str("версия", version).
		Str("была", update.FromVersion).
		Msg("OTA: обновление завершено успешно")
}

// fail помечает обновление как неуспешное.
func (s *FirmwareService) fail(update *OTAUpdate, step string, err error) {
	update.State = "failed"
	update.Step = step
	update.Message = err.Error()
	update.FinishedAt = time.Now()
	s.setUpdate(update)

	log.Error().Err(err).Str("контроллер", update.ControllerID.String()[:8]).
		Str("шаг", step).Msg("OTA: обновление не удалось")
}

// firmwareAdapter находит адаптер OTA для контроллера.
func (s *ACSService) firmwareAdapter(ctx context.Context, ctrl *domain.ACSController) (acs.FirmwareManager, error) {
	adapter, err := s.manager.GetAdapter(ctrl.Vendor, ctrl)
	if err != nil {
		return nil, fmt.Errorf("нет адаптера для %s: %w", ctrl.Vendor, err)
	}

	fm, ok := acs.FirmwareFor(adapter)
	if !ok {
		return nil, fmt.Errorf("контроллер %s не поддерживает обновление по OTA", ctrl.Vendor)
	}
	return fm, nil
}

// GetFirmwareVersion читает версию прошивки контроллера.
func (s *FirmwareService) GetFirmwareVersion(ctx context.Context, controllerID uuid.UUID) (*acs.FirmwareInfo, error) {
	ctrl, err := s.acsSvc.GetController(ctx, controllerID)
	if err != nil {
		return nil, fmt.Errorf("контроллер не найден: %w", err)
	}

	adapter, err := s.acsSvc.firmwareAdapter(ctx, ctrl)
	if err != nil {
		return nil, err
	}
	return adapter.GetFirmwareInfo(ctx)
}

// fileSHA256 считает контрольную сумму файла.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
