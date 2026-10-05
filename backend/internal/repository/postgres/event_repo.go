package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
	"github.com/rs/zerolog/log"
)

type EventRepo struct {
	db *pgxpool.Pool
}

func NewEventRepo(db *pgxpool.Pool) *EventRepo {
	return &EventRepo{db: db}
}

// List возвращает события с пагинацией. cameraID и objectClass —
// необязательные фильтры; пустые значения означают «без фильтра».
//
// from/to ограничивают период. Нужны архиву: чтобы показать детекции
// поверх записи, интерфейс запрашивает события только за её время, а не
// всю историю камеры.
func (r *EventRepo) List(ctx context.Context, cameraID *uuid.UUID, objectClass string,
	from, to *time.Time, search string, page, pageSize int) ([]domain.DetectionEvent, int64, error) {
	where := "WHERE 1=1"
	args := []interface{}{}
	argIdx := 1

	if cameraID != nil {
		where += " AND camera_id = $" + itoa(argIdx)
		args = append(args, cameraID.String())
		argIdx++
	}

	// Фильтр по классу объекта: нужен для просмотра «только номера» или
	// «только люди» — иначе нужный кадр теряется среди остальных.
	if objectClass != "" {
		where += " AND object_class = $" + itoa(argIdx)
		args = append(args, objectClass)
		argIdx++
	}

	if from != nil {
		where += " AND timestamp >= $" + itoa(argIdx)
		args = append(args, *from)
		argIdx++
	}
	if to != nil {
		where += " AND timestamp <= $" + itoa(argIdx)
		args = append(args, *to)
		argIdx++
	}

	// Поиск по распознанному номеру и по имени из справочника. Номер хранится
	// в метаданных события (ключ plate_text), имя — в отдельной колонке.
	//
	// Сравнение по шаблону «содержит»: оператор помнит фрагмент номера
	// («147», «АА47»), а не строку целиком. На десятках тысяч записей это
	// последовательный просмотр; при росте базы сюда нужен индекс pg_trgm.
	if search != "" {
		pattern := "%" + search + "%"
		where += " AND (metadata->>'plate_text' ILIKE $" + itoa(argIdx) +
			" OR COALESCE(matched_name, '') ILIKE $" + itoa(argIdx) + ")"
		args = append(args, pattern)
		argIdx++
	}

	// Total count
	var total int64
	countQuery := "SELECT COUNT(*) FROM detection_events " + where
	if err := r.db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	// snapshot_path и thumbnail_path могут быть NULL, а в domain это string:
	// без COALESCE rows.Scan падает, и запись молча теряется (было видно
	// «total есть, а список пуст»).
	query := `SELECT e.id, e.camera_id, e.timestamp, e.object_class, e.confidence,
		e.bbox, e.track_id, COALESCE(e.snapshot_path,''), COALESCE(e.thumbnail_path,''),
		COALESCE(e.metadata,'{}'),
		COALESCE(c.name, '') as camera_name,
		COALESCE(e.match_type,'unknown'), e.matched_id, COALESCE(e.matched_name,'')
		FROM detection_events e
		LEFT JOIN cameras c ON c.id = e.camera_id ` + where +
		` ORDER BY e.timestamp DESC LIMIT $` + itoa(argIdx) + ` OFFSET $` + itoa(argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events := make([]domain.DetectionEvent, 0)
	for rows.Next() {
		var ev domain.DetectionEvent
		var bbox, metadata []byte
		if err := rows.Scan(&ev.ID, &ev.CameraID, &ev.Timestamp, &ev.ObjectClass,
			&ev.Confidence, &bbox, &ev.TrackID, &ev.SnapshotPath, &ev.ThumbnailPath,
			&metadata, &ev.CameraName, &ev.MatchType, &ev.MatchedID, &ev.MatchedName); err != nil {
			// Ошибку не глотаем молча: без лога причина «пустого списка»
			// при ненулевом total неочевидна.
			log.Warn().Err(err).Msg("не удалось прочитать событие детекции")
			continue
		}
		if bbox != nil {
			json.Unmarshal(bbox, &ev.BBox)
		}
		if metadata != nil {
			json.Unmarshal(metadata, &ev.Metadata)
		}
		events = append(events, ev)
	}
	return events, total, nil
}

// CrossingStats возвращает число пересечений линии за период, отдельно по
// направлениям.
//
// Считается в базе, а не в интерфейсе: за сутки по одной камере событий
// бывают тысячи, и выгружать их целиком ради двух чисел нельзя.
// Признак направления лежит в metadata (ключ crossing), его ставит детектор
// в момент пересечения.
func (r *EventRepo) CrossingStats(ctx context.Context, cameraID uuid.UUID,
	since time.Time) (forward, backward int, err error) {
	err = r.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE metadata->>'crossing' = 'forward'),
			COUNT(*) FILTER (WHERE metadata->>'crossing' = 'backward')
		FROM detection_events
		WHERE camera_id = $1 AND timestamp >= $2 AND metadata ? 'crossing'
	`, cameraID, since).Scan(&forward, &backward)
	return forward, backward, err
}

func (r *EventRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.DetectionEvent, error) {
	var ev domain.DetectionEvent
	var bbox, metadata []byte
	err := r.db.QueryRow(ctx, `
		SELECT e.id, e.camera_id, e.timestamp, e.object_class, e.confidence,
		e.bbox, e.track_id, COALESCE(e.snapshot_path,''), COALESCE(e.thumbnail_path,''),
		COALESCE(e.metadata,'{}'),
		COALESCE(c.name, '') as camera_name,
		COALESCE(e.match_type,'unknown'), e.matched_id, COALESCE(e.matched_name,'')
		FROM detection_events e
		LEFT JOIN cameras c ON c.id = e.camera_id
		WHERE e.id = $1
	`, id).Scan(&ev.ID, &ev.CameraID, &ev.Timestamp, &ev.ObjectClass,
		&ev.Confidence, &bbox, &ev.TrackID, &ev.SnapshotPath, &ev.ThumbnailPath,
		&metadata, &ev.CameraName, &ev.MatchType, &ev.MatchedID, &ev.MatchedName)
	if err != nil {
		return nil, err
	}
	if bbox != nil {
		json.Unmarshal(bbox, &ev.BBox)
	}
	if metadata != nil {
		json.Unmarshal(metadata, &ev.Metadata)
	}
	return &ev, nil
}

func itoa(i int) string {
	return strconv.Itoa(i)
}
