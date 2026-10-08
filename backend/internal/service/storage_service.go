package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nvr/backend/internal/domain"
	miniorepo "github.com/nvr/backend/internal/repository/minio"
	"github.com/nvr/backend/internal/repository/postgres"
)

// StorageService сохраняет снимки и записи в выбранное хранилище.
// Поддерживает MinIO/S3 и локальный диск — выбор хранится в server_settings.
type StorageService struct {
	settings  *postgres.DetectionSettingsRepo
	videoRepo *miniorepo.VideoRepo
}

func NewStorageService(settings *postgres.DetectionSettingsRepo, videoRepo *miniorepo.VideoRepo) *StorageService {
	return &StorageService{settings: settings, videoRepo: videoRepo}
}

// storageConfig выбирает хранилище для одного вида данных (записи, снимки).
//
// Порядок выбора: сначала явная настройка оператора, затем — если настройки
// нет — локальный диск при недоступном MinIO, и только потом MinIO.
//
// Локальный диск стоит выше MinIO именно потому, что MinIO может быть не
// настроен вовсе: публичные образы MinIO убраны из реестров, и при установке
// «одной командой» контейнера с ним может просто не быть. Раньше в этом
// случае возвращалась ошибка «minio недоступен, а хранилище настроено на
// minio» — снимки и записи не сохранялись, а выглядело это как исправная
// работа. Теперь сервер сам пишет на диск.
//
// Явная настройка из интерфейса важнее: оператор мог сознательно указать
// MinIO, и подменять его выбор нельзя.
func (s *StorageService) storageConfig(ctx context.Context, pick func(domain.ServerSettings) domain.StorageConfig, localPath string) domain.StorageConfig {
	var configured domain.StorageConfig
	if cfg, err := s.settings.GetServerSettings(ctx); err == nil {
		configured = pick(*cfg)
	}
	return chooseStorage(configured, s.videoRepo != nil, localPath)
}

// chooseStorage — сам выбор хранилища, отдельно от базы и MinIO-клиента,
// чтобы его можно было проверить тестом без запуска сервисов.
//
// configured — настройка оператора (пустая, если её не задавали).
// hasMinio — удалось ли подключиться к S3 при старте сервиса.
func chooseStorage(configured domain.StorageConfig, hasMinio bool, localPath string) domain.StorageConfig {
	// Явный выбор оператора важнее: он мог сознательно включить MinIO,
	// и подменять его решение «на своё усмотрение» нельзя.
	if configured.Backend != "" {
		return configured
	}
	if hasMinio {
		return domain.StorageConfig{Backend: "minio", LocalPath: localPath}
	}
	return domain.StorageConfig{Backend: "local", LocalPath: localPath}
}

// SnapshotKey формирует путь снимка: <camera>/<дата>/<время>_<класс>.jpg
func SnapshotKey(cameraID uuid.UUID, t time.Time, objectClass string) string {
	return fmt.Sprintf("snapshots/%s/%s/%s_%s.jpg",
		cameraID.String(),
		t.UTC().Format("2006-01-02"),
		t.UTC().Format("150405.000"),
		sanitizeClass(objectClass),
	)
}

// localSnapshotKey — тот же путь, но без ведущего "snapshots/".
// Для локального хранилища базовый каталог уже задаёт смысл («снимки лежат здесь»),
// иначе получается путь вида <local_path>/snapshots/snapshots/...
func localSnapshotKey(cameraID uuid.UUID, t time.Time, objectClass string) string {
	return fmt.Sprintf("%s/%s/%s_%s.jpg",
		cameraID.String(),
		t.UTC().Format("2006-01-02"),
		t.UTC().Format("150405.000"),
		sanitizeClass(objectClass),
	)
}

// sanitizeClass убирает из имени класса всё, кроме букв, цифр и дефиса,
// чтобы имя файла было безопасным.
func sanitizeClass(s string) string {
	if s == "" {
		return "object"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// SaveSnapshot сохраняет кадр события и возвращает путь для записи в БД.
// Пустое значение пути означает, что снимок не сохранён (не ошибка).
func (s *StorageService) SaveSnapshot(ctx context.Context, cameraID uuid.UUID, t time.Time, objectClass string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}

	st := s.storageConfig(ctx, func(c domain.ServerSettings) domain.StorageConfig { return c.Snapshots },
		"/var/lib/nvr/snapshots")

	switch st.Backend {
	case "local":
		path, err := saveLocal(st.LocalPath, localSnapshotKey(cameraID, t, objectClass), data)
		if err != nil {
			return "", err
		}
		return path, nil
	default:
		if s.videoRepo == nil {
			return "", fmt.Errorf("minio недоступен, а хранилище снимков настроено на minio")
		}
		key := SnapshotKey(cameraID, t, objectClass)
		if err := s.videoRepo.Upload(ctx, key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
			return "", err
		}
		// Для MinIO храним ключ, а ссылка выдаётся при чтении.
		return "minio:" + key, nil
	}
}

// SaveReferencePhoto сохраняет эталонный снимок (лицо или номер автомобиля).
//
// Отличается от SaveSnapshot тем, что снимок не привязан к камере и событию —
// он хранится рядом со справочником: reference/faces/<uuid>.jpg.
func (s *StorageService) SaveReferencePhoto(ctx context.Context, kind string, id uuid.UUID, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}

	st := s.storageConfig(ctx, func(c domain.ServerSettings) domain.StorageConfig { return c.Snapshots },
		"/var/lib/nvr/snapshots")

	key := fmt.Sprintf("reference/%s/%s.jpg", kind, id.String())

	switch st.Backend {
	case "local":
		// Для локального хранилища базовый путь уже смысловой,
		// поэтому префикс "reference" не дублируем.
		path, err := saveLocal(st.LocalPath, key, data)
		if err != nil {
			return "", err
		}
		return path, nil
	default:
		if s.videoRepo == nil {
			return "", fmt.Errorf("minio недоступен, а хранилище настроено на minio")
		}
		if err := s.videoRepo.Upload(ctx, key, bytes.NewReader(data), int64(len(data)), "image/jpeg"); err != nil {
			return "", err
		}
		return "minio:" + key, nil
	}
}

// ReadStoredFile отдаёт содержимое файла по сохранённому пути
// (`minio:<key>` или `local:<abspath>`). Нужен для показа эталонных снимков
// через бэкенд: presigned-ссылка MinIO привязана к Host и не работает извне.
func (s *StorageService) ReadStoredFile(ctx context.Context, storedPath string) ([]byte, int64, error) {
	if storedPath == "" {
		return nil, 0, fmt.Errorf("пустой путь")
	}
	if strings.HasPrefix(storedPath, "local:") {
		data, err := ReadLocalSnapshot(storedPath)
		if err != nil {
			return nil, 0, err
		}
		return data, int64(len(data)), nil
	}
	if s.videoRepo == nil {
		return nil, 0, fmt.Errorf("minio недоступен")
	}
	key := strings.TrimPrefix(storedPath, "minio:")
	return s.videoRepo.GetObject(ctx, key)
}

// saveLocal сохраняет файл на диск, создавая каталоги при необходимости.
func saveLocal(basePath, key string, data []byte) (string, error) {
	if basePath == "" {
		basePath = "/var/lib/nvr"
	}
	// Базовый путь — доверенный (из настроек), key формируется нами,
	// поэтому обход каталога через "../" невозможен.
	full := filepath.Join(basePath, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("create dir for %q: %w", full, err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", fmt.Errorf("write %q: %w", full, err)
	}
	return "local:" + full, nil
}

// ReadLocalSnapshot читает файл из локального хранилища (для отдачи клиенту).
func ReadLocalSnapshot(storedPath string) ([]byte, error) {
	p := strings.TrimPrefix(storedPath, "local:")
	if p == storedPath {
		return nil, fmt.Errorf("not a local path")
	}
	return os.ReadFile(p)
}

// RecordingKey формирует путь записи: recordings/<camera>/<дата>/<время>.mp4
func RecordingKey(cameraID uuid.UUID, start time.Time) string {
	return fmt.Sprintf("recordings/%s/%s/%s.mp4",
		cameraID.String(),
		start.UTC().Format("2006-01-02"),
		start.UTC().Format("150405"),
	)
}

// localRecordingKey — путь для локального хранилища (без ведущего "recordings/").
func localRecordingKey(cameraID uuid.UUID, start time.Time) string {
	return fmt.Sprintf("%s/%s/%s.mp4",
		cameraID.String(),
		start.UTC().Format("2006-01-02"),
		start.UTC().Format("150405"),
	)
}

// SaveClip отправляет готовый видеофайл в хранилище.
// Возвращает сохранённый путь (с префиксом хранилища) и размер файла.
func (s *StorageService) SaveClip(ctx context.Context, clipPath string) (string, int64, error) {
	fi, err := os.Stat(clipPath)
	if err != nil {
		return "", 0, fmt.Errorf("stat clip: %w", err)
	}
	size := fi.Size()

	// Камеру и время берём из пути буфера: <bufferDir>/<cameraID>/clip_<время>.mp4
	dir := filepath.Base(filepath.Dir(clipPath))
	cameraID, err := uuid.Parse(dir)
	if err != nil {
		return "", 0, fmt.Errorf("не удалось определить камеру из пути %q: %w", clipPath, err)
	}
	start, err := time.ParseInLocation("20060102_150405",
		strings.TrimSuffix(strings.TrimPrefix(filepath.Base(clipPath), "clip_"), ".mp4"), time.Local)
	if err != nil {
		start = fi.ModTime()
	}

	st := s.storageConfig(ctx, func(c domain.ServerSettings) domain.StorageConfig { return c.Storage },
		"/var/lib/nvr/recordings")

	switch st.Backend {
	case "local":
		key := localRecordingKey(cameraID, start)
		dst, err := moveLocal(st.LocalPath, key, clipPath)
		if err != nil {
			return "", 0, err
		}
		return dst, size, nil
	default:
		if s.videoRepo == nil {
			return "", 0, fmt.Errorf("minio недоступен, а хранилище записей настроено на minio")
		}
		key := RecordingKey(cameraID, start)
		f, err := os.Open(clipPath)
		if err != nil {
			return "", 0, fmt.Errorf("open clip: %w", err)
		}
		defer f.Close()

		if err := s.videoRepo.Upload(ctx, key, f, size, "video/mp4"); err != nil {
			return "", 0, err
		}
		return "minio:" + key, size, nil
	}
}

// moveLocal переносит файл в каталог локального хранилища.
// Файл именно перемещается, чтобы не занимать место дважды.
func moveLocal(basePath, key, src string) (string, error) {
	if basePath == "" {
		basePath = "/var/lib/nvr/recordings"
	}
	full := filepath.Join(basePath, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("create dir for %q: %w", full, err)
	}
	if err := os.Rename(src, full); err != nil {
		// Разные файловые системы — копируем и удаляем исходник
		data, rdErr := os.ReadFile(src)
		if rdErr != nil {
			return "", fmt.Errorf("move %q: %w", full, err)
		}
		if wrErr := os.WriteFile(full, data, 0o644); wrErr != nil {
			return "", fmt.Errorf("write %q: %w", full, wrErr)
		}
		os.Remove(src)
	}
	return "local:" + full, nil
}

// RecordingURL возвращает ссылку на файл записи.
func (s *StorageService) RecordingURL(ctx context.Context, storedPath string) (string, error) {
	if storedPath == "" {
		return "", nil
	}
	if strings.HasPrefix(storedPath, "minio:") {
		if s.videoRepo == nil {
			return "", fmt.Errorf("minio недоступен")
		}
		return s.videoRepo.PresignedURL(ctx, strings.TrimPrefix(storedPath, "minio:"), time.Hour)
	}
	return storedPath, nil
}

// ReadLocalRecording читает файл записи с локального диска (для отдачи клиенту).
func ReadLocalRecording(storedPath string) ([]byte, error) {
	p, err := LocalRecordingPath(storedPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// LocalRecordingPath превращает путь вида local:/... в абсолютный путь на диске.
// Нужен, чтобы отдавать файл через http.ServeFile — он умеет Range и кеширование.
func LocalRecordingPath(storedPath string) (string, error) {
	p := strings.TrimPrefix(storedPath, "local:")
	if p == storedPath || p == "" {
		return "", fmt.Errorf("not a local path")
	}
	return p, nil
}
