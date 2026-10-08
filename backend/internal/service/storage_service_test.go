package service

import (
	"testing"

	"github.com/nvr/backend/internal/domain"
)

// TestChooseStorage_NoMinioFallsBackToDisk проверяет главный случай: сервер
// поднят без MinIO (публичные образы MinIO убраны из реестров, и при установке
// «одной командой» контейнера с ним может не быть).
//
// Ожидание — локальный диск, а не пустой backend. Раньше в этом случае
// сохранение возвращало ошибку «minio недоступен», и снимки с записями
// просто не сохранялись, оставаясь незаметными для оператора.
func TestChooseStorage_NoMinioFallsBackToDisk(t *testing.T) {
	got := chooseStorage(domain.StorageConfig{}, false, "/var/lib/nvr/snapshots")

	if got.Backend != "local" {
		t.Fatalf("без MinIO ожидался локальный диск, получено %q", got.Backend)
	}
	if got.LocalPath != "/var/lib/nvr/snapshots" {
		t.Fatalf("путь локального хранилища потерян: %q", got.LocalPath)
	}
}

// TestChooseStorage_MinioWhenAvailable — при рабочем S3 выбор остаётся
// прежним, чтобы не менять поведение уже настроенных установок.
func TestChooseStorage_MinioWhenAvailable(t *testing.T) {
	got := chooseStorage(domain.StorageConfig{}, true, "/var/lib/nvr/recordings")

	if got.Backend != "minio" {
		t.Fatalf("при доступном MinIO ожидался minio, получено %q", got.Backend)
	}
}

// TestChooseStorage_ExplicitSettingWins — настройка оператора из интерфейса
// главнее выбора по умолчанию, даже если MinIO доступен.
//
// Иначе оператор, выбравший локальный диск, после перезапуска снова получил
// бы записи в S3 — и не понял бы, почему настройка «не держится».
func TestChooseStorage_ExplicitSettingWins(t *testing.T) {
	configured := domain.StorageConfig{Backend: "local", LocalPath: "/data/archive"}

	got := chooseStorage(configured, true, "/var/lib/nvr/recordings")

	if got.Backend != "local" || got.LocalPath != "/data/archive" {
		t.Fatalf("настройка оператора перебита значением по умолчанию: %+v", got)
	}
}
