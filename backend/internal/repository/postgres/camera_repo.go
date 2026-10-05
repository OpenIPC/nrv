package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
)

type CameraRepo struct {
	db *pgxpool.Pool
}

func NewCameraRepo(db *pgxpool.Pool) *CameraRepo {
	return &CameraRepo{db: db}
}

func (r *CameraRepo) List(ctx context.Context) ([]domain.Camera, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, rtsp_url,
			COALESCE(main_stream, '') as main_stream,
			COALESCE(sub_stream, '') as sub_stream,
			COALESCE(ip, '') as ip,
			COALESCE(mac, '') as mac,
			COALESCE(firmware, '') as firmware,
			COALESCE(vendor, 'unknown') as vendor,
			site_id, COALESCE(wg_ip::text, '') as wg_ip, status, hw_info, settings,
			channel_number, created_at, updated_at
		FROM cameras ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cameras := make([]domain.Camera, 0)
	for rows.Next() {
		var c domain.Camera
		var hwInfo, settings []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.RTSPUrl, &c.MainStream, &c.SubStream, &c.IP, &c.MAC, &c.Firmware, &c.Vendor,
			&c.SiteID, &c.WGIP,
			&c.Status, &hwInfo, &settings, &c.ChannelNumber, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if hwInfo != nil {
			json.Unmarshal(hwInfo, &c.HWInfo)
		}
		if settings != nil {
			json.Unmarshal(settings, &c.Settings)
		}
		applySettings(&c)
		cameras = append(cameras, c)
	}
	return cameras, nil
}

// applySettings переносит значения из JSONB-поля settings в поля структуры.
// Отдельные колонки под них не заводим: набор настроек будет расширяться,
// а схему БД менять при каждом новом флаге неудобно.
func applySettings(c *domain.Camera) {
	if c.Settings == nil {
		return
	}
	if v, ok := c.Settings["ptz"].(bool); ok {
		c.PTZ = v
	}
}

func (r *CameraRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Camera, error) {
	var c domain.Camera
	var hwInfo, settings []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, name, rtsp_url,
			COALESCE(main_stream, '') as main_stream,
			COALESCE(sub_stream, '') as sub_stream,
			COALESCE(ip, '') as ip,
			COALESCE(mac, '') as mac,
			COALESCE(firmware, '') as firmware,
			COALESCE(vendor, 'unknown') as vendor,
			site_id, COALESCE(wg_ip::text, '') as wg_ip, status, hw_info, settings,
			channel_number, created_at, updated_at
		FROM cameras WHERE id = $1
	`, id).Scan(&c.ID, &c.Name, &c.RTSPUrl, &c.MainStream, &c.SubStream, &c.IP, &c.MAC, &c.Firmware, &c.Vendor,
		&c.SiteID, &c.WGIP,
		&c.Status, &hwInfo, &settings, &c.ChannelNumber, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if hwInfo != nil {
		json.Unmarshal(hwInfo, &c.HWInfo)
	}
	if settings != nil {
		json.Unmarshal(settings, &c.Settings)
	}
	applySettings(&c)
	return &c, nil
}

// ListByIDs читает камеры по списку идентификаторов.
//
// Нужен плану помещений: там состояние показывается сразу для десятков
// камер, и запрос на каждую превратил бы открытие схемы в серию обращений
// к базе. Возвращает только найденные записи: камера могла быть удалена,
// а точка на плане осталась — это и есть признак «устройство удалено».
func (r *CameraRepo) ListByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Camera, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := r.db.Query(ctx, `
		SELECT id, name, rtsp_url,
			COALESCE(main_stream, '') as main_stream,
			COALESCE(sub_stream, '') as sub_stream,
			COALESCE(ip, '') as ip,
			COALESCE(mac, '') as mac,
			COALESCE(firmware, '') as firmware,
			COALESCE(vendor, 'unknown') as vendor,
			site_id, COALESCE(wg_ip::text, '') as wg_ip, status, hw_info, settings,
			channel_number, created_at, updated_at
		FROM cameras WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cameras := make([]domain.Camera, 0, len(ids))
	for rows.Next() {
		var c domain.Camera
		var hwInfo, settings []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.RTSPUrl, &c.MainStream, &c.SubStream,
			&c.IP, &c.MAC, &c.Firmware, &c.Vendor, &c.SiteID, &c.WGIP,
			&c.Status, &hwInfo, &settings, &c.ChannelNumber,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if hwInfo != nil {
			json.Unmarshal(hwInfo, &c.HWInfo)
		}
		if settings != nil {
			json.Unmarshal(settings, &c.Settings)
		}
		applySettings(&c)
		cameras = append(cameras, c)
	}
	return cameras, rows.Err()
}

func (r *CameraRepo) Create(ctx context.Context, cam *domain.Camera) error {
	hwInfo, _ := json.Marshal(cam.HWInfo)
	settings, _ := json.Marshal(cam.Settings)
	_, err := r.db.Exec(ctx, `
		INSERT INTO cameras (id, name, rtsp_url, main_stream, sub_stream, ip, mac, firmware, vendor, site_id, wg_ip, status, hw_info, settings, channel_number, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, '')::inet, $12, $13, $14, $15, $16, $17)
	`, cam.ID, cam.Name, cam.RTSPUrl, cam.MainStream, cam.SubStream, cam.IP, cam.MAC, cam.Firmware,
		vendorOrDefault(cam.Vendor),
		cam.SiteID, cam.WGIP,
		cam.Status, hwInfo, settings, cam.ChannelNumber, cam.CreatedAt, cam.UpdatedAt)
	return err
}

// vendorOrDefault — значение вендора для записи в базу.
//
// Пустая строка превращается в 'unknown': в базе «неизвестно» — это
// отдельное значение, а не отсутствие данных, и по нему принимается
// решение не показывать настройки OpenIPC.
func vendorOrDefault(v domain.Vendor) string {
	if v == "" {
		return string(domain.VendorUnknown)
	}
	return string(v)
}

func (r *CameraRepo) Update(ctx context.Context, cam *domain.Camera) error {
	hwInfo, _ := json.Marshal(cam.HWInfo)
	settings, _ := json.Marshal(cam.Settings)
	cam.UpdatedAt = time.Now()
	_, err := r.db.Exec(ctx, `
		UPDATE cameras SET name=$2, rtsp_url=$3, main_stream=$4, sub_stream=$5, ip=$6, mac=$7, firmware=$8,
		vendor=$9, site_id=$10, wg_ip=NULLIF($11, '')::inet, status=$12,
		hw_info=$13, settings=$14, channel_number=$15, updated_at=$16 WHERE id=$1
	`, cam.ID, cam.Name, cam.RTSPUrl, cam.MainStream, cam.SubStream, cam.IP, cam.MAC, cam.Firmware,
		vendorOrDefault(cam.Vendor), cam.SiteID, cam.WGIP,
		cam.Status, hwInfo, settings, cam.ChannelNumber, cam.UpdatedAt)
	return err
}

func (r *CameraRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM cameras WHERE id = $1`, id)
	return err
}

// NextFreeChannel возвращает наименьший свободный номер канала.
//
// Номер нужен при добавлении камеры, чтобы она сразу попала в список
// внешнего доступа: без него оператору пришлось бы открывать карточку
// и задавать номер вручную.
//
// Берём именно наименьший свободный номер, а не «максимум + 1»:
// удалённые камеры освобождают номера, и после нескольких удалений
// нумерация уезжала бы вверх, оставляя дыры. Внешним системам удобнее,
// когда каналы идут подряд.
func (r *CameraRepo) NextFreeChannel(ctx context.Context) (int, error) {
	var next int
	err := r.db.QueryRow(ctx, `
		SELECT COALESCE(MIN(candidate), 1)
		FROM (
			SELECT gs AS candidate
			FROM generate_series(1, COALESCE((SELECT MAX(channel_number) FROM cameras), 0) + 1) AS gs
			WHERE NOT EXISTS (
				SELECT 1 FROM cameras WHERE channel_number = gs
			)
		) AS free
	`).Scan(&next)
	if err != nil {
		return 0, err
	}
	return next, nil
}

// UpdateStatus обновляет только поле status (используется монитором доступности).
func (r *CameraRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := r.db.Exec(ctx, `UPDATE cameras SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

// ListForStatusCheck возвращает минимальный набор полей для проверки доступности.
// ListForStatusCheck возвращает камеры для монитора статуса.
//
// Поле settings читается обязательно: монитор не только выставляет статус,
// но и восстанавливает пропавшие пути go2rtc. Для этого ему нужны учётные
// данные камеры — без них RTSP-ссылка уходит без логина и пароля, камера
// отвечает 401, и путь остаётся нерабочим. Симптом: камеры с учётными
// данными внутри URL работают, а с данными в отдельных полях — нет.
func (r *CameraRepo) ListForStatusCheck(ctx context.Context) ([]domain.Camera, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, COALESCE(ip, '') as ip,
		       COALESCE(main_stream, '') as main_stream,
		       COALESCE(sub_stream, '') as sub_stream,
		       status, settings
		FROM cameras
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cameras := make([]domain.Camera, 0)
	for rows.Next() {
		var c domain.Camera
		var settings []byte
		if err := rows.Scan(&c.ID, &c.Name, &c.IP, &c.MainStream, &c.SubStream,
			&c.Status, &settings); err != nil {
			return nil, err
		}
		if settings != nil {
			json.Unmarshal(settings, &c.Settings)
		}
		cameras = append(cameras, c)
	}
	return cameras, rows.Err()
}
