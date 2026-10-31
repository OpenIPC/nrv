package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	natspkg "github.com/nvr/backend/internal/nats"
	"github.com/rs/zerolog/log"
)

// SavedSearch — сохранённый поиск по архиву.
//
// Критерии хранятся разобранными (классы, цвета, типы детекций, камеры):
// сравнение с новым событием должно быть сравнением массивов, а не разбором
// текста на каждом событии. Исходный текст остаётся для показа оператору.
type SavedSearch struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	QueryText       string      `json:"query_text"`
	ObjectClasses   []string    `json:"object_classes"`
	Colors          []string    `json:"colors"`
	DetectionKinds  []string    `json:"detection_kinds"`
	CameraIDs       []uuid.UUID `json:"camera_ids"`
	Notify          bool        `json:"notify"`
	TriggeredCount  int         `json:"triggered_count"`
	LastTriggeredAt *time.Time  `json:"last_triggered_at,omitempty"`
	CreatedAt       time.Time   `json:"created_at"`
}

// SavedSearchInput — что приходит из интерфейса при создании.
type SavedSearchInput struct {
	Name           string      `json:"name"`
	QueryText      string      `json:"query_text"`
	ObjectClasses  []string    `json:"object_classes"`
	Colors         []string    `json:"colors"`
	DetectionKinds []string    `json:"detection_kinds"`
	CameraIDs      []uuid.UUID `json:"camera_ids"`
	Notify         *bool       `json:"notify,omitempty"`
}

// EventFeatures — то, что известно о новом событии для сравнения.
//
// Отдельная структура, а не domain.DetectionEvent: сервису не нужен весь
// набор полей события, а зависимость от домена потянула бы за собой
// пакет nats и замкнула бы цикл импортов.
type EventFeatures struct {
	CameraID    uuid.UUID
	ObjectClass string
	Color       string
	// Kind — тип детекции: object, line, plate, face.
	Kind string
	Time time.Time
}

// SavedSearchService хранит сохранённые поиски и решает, какие из них
// совпали с новым событием.
//
// Список держим в памяти и перечитываем раз в несколько секунд: событий
// бывает несколько в минуту, и обращаться к базе на каждое — лишняя работа
// на ровном месте. Ту же схему использует кэш настроек детекции.
type SavedSearchService struct {
	db *pgxpool.Pool

	mu          sync.RWMutex
	cache       []SavedSearch
	loadedAt    time.Time
	reloadEvery time.Duration
}

// NewSavedSearchService создаёт сервис с пустым кэшем.
func NewSavedSearchService(db *pgxpool.Pool) *SavedSearchService {
	return &SavedSearchService{db: db, reloadEvery: 15 * time.Second}
}

// List возвращает сохранённые поиски.
func (s *SavedSearchService) List(ctx context.Context) ([]SavedSearch, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, query_text, object_classes, colors, detection_kinds,
		       camera_ids, notify, triggered_count, last_triggered_at, created_at
		FROM saved_searches
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]SavedSearch, 0, 8)
	for rows.Next() {
		item, scanErr := scanSavedSearch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Create добавляет сохранённый поиск.
func (s *SavedSearchService) Create(ctx context.Context, input SavedSearchInput, createdBy uuid.UUID) (*SavedSearch, error) {
	notify := true
	if input.Notify != nil {
		notify = *input.Notify
	}

	row := s.db.QueryRow(ctx, `
		INSERT INTO saved_searches
			(name, query_text, object_classes, colors, detection_kinds, camera_ids, notify, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, name, query_text, object_classes, colors, detection_kinds,
		          camera_ids, notify, triggered_count, last_triggered_at, created_at`,
		strings.TrimSpace(input.Name),
		strings.TrimSpace(input.QueryText),
		emptyIfNil(input.ObjectClasses),
		emptyIfNil(input.Colors),
		emptyIfNil(input.DetectionKinds),
		emptyIfNil(input.CameraIDs),
		notify,
		nullableUUID(createdBy),
	)

	item, err := scanSavedSearch(row)
	if err != nil {
		return nil, err
	}
	s.invalidate()
	return &item, nil
}

// SetNotify включает или выключает уведомления по поиску.
func (s *SavedSearchService) SetNotify(ctx context.Context, id uuid.UUID, notify bool) error {
	_, err := s.db.Exec(ctx, `UPDATE saved_searches SET notify = $2 WHERE id = $1`, id, notify)
	if err == nil {
		s.invalidate()
	}
	return err
}

// Delete удаляет поиск.
func (s *SavedSearchService) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM saved_searches WHERE id = $1`, id)
	if err == nil {
		s.invalidate()
	}
	return err
}

// Match возвращает поиски, которым соответствует событие.
//
// Вызывается на каждое сохранённое событие, поэтому работает по кэшу и
// только со сравнением массивов.
func (s *SavedSearchService) Match(ev EventFeatures) []SavedSearch {
	s.reload()

	s.mu.RLock()
	defer s.mu.RUnlock()

	hits := make([]SavedSearch, 0, 2)
	for _, search := range s.cache {
		if !search.Notify {
			continue
		}
		if !matchesAll(search.CameraIDs, func(id uuid.UUID) bool { return id == ev.CameraID }) {
			continue
		}
		if !matchesStrings(search.ObjectClasses, ev.ObjectClass) {
			continue
		}
		if !matchesStrings(search.Colors, ev.Color) {
			continue
		}
		if !matchesStrings(search.DetectionKinds, ev.Kind) {
			continue
		}
		hits = append(hits, search)
	}
	return hits
}

// MarkTriggered увеличивает счётчик срабатываний.
//
// Вызывается после отправки уведомления, а не при каждом совпадении:
// счётчик должен означать «сколько раз сообщили», иначе он не говорит
// ничего о шуме.
func (s *SavedSearchService) MarkTriggered(ctx context.Context, id uuid.UUID) {
	_, err := s.db.Exec(ctx, `
		UPDATE saved_searches
		SET triggered_count = triggered_count + 1, last_triggered_at = now()
		WHERE id = $1`, id)
	if err != nil {
		log.Warn().Err(err).Str("saved_search", id.String()).Msg("не удалось обновить счётчик поиска")
	}
}

// --- кэш ---

func (s *SavedSearchService) reload() {
	s.mu.RLock()
	fresh := time.Since(s.loadedAt) < s.reloadEvery
	s.mu.RUnlock()
	if fresh {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	items, err := s.List(ctx)
	if err != nil {
		// База недоступна — работаем по старому списку. Ошибка не должна
		// мешать сохранять события.
		log.Warn().Err(err).Msg("не удалось перечитать сохранённые поиски")
		return
	}

	s.mu.Lock()
	s.cache = items
	s.loadedAt = time.Now()
	s.mu.Unlock()
}

func (s *SavedSearchService) invalidate() {
	s.mu.Lock()
	s.loadedAt = time.Time{}
	s.mu.Unlock()
}

// rowScanner — общий интерфейс pgx.Row и pgx.Rows по методу Scan.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSavedSearch(row rowScanner) (SavedSearch, error) {
	var item SavedSearch
	err := row.Scan(&item.ID, &item.Name, &item.QueryText, &item.ObjectClasses,
		&item.Colors, &item.DetectionKinds, &item.CameraIDs, &item.Notify,
		&item.TriggeredCount, &item.LastTriggeredAt, &item.CreatedAt)
	if err != nil {
		return SavedSearch{}, err
	}
	if item.ObjectClasses == nil {
		item.ObjectClasses = []string{}
	}
	if item.Colors == nil {
		item.Colors = []string{}
	}
	if item.DetectionKinds == nil {
		item.DetectionKinds = []string{}
	}
	if item.CameraIDs == nil {
		item.CameraIDs = []uuid.UUID{}
	}
	return item, nil
}

// matchesStrings сообщает, подходит ли значение под список.
// Пустой список — «не ограничивать».
func matchesStrings(allowed []string, value string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func matchesAll(ids []uuid.UUID, predicate func(uuid.UUID) bool) bool {
	if len(ids) == 0 {
		return true
	}
	for _, id := range ids {
		if predicate(id) {
			return true
		}
	}
	return false
}

func emptyIfNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// SavedSearchAdapter связывает сервис поиска с подписчиком детекций.
//
// Отдельный тип, потому что подписчик не должен зависеть от этого пакета
// (сервис сам импортирует nats — вебхуки), а Go не сопоставляет структуры
// по форме: интерфейс требует именно свой тип результата.
type SavedSearchAdapter struct {
	Service *SavedSearchService
}

// MatchEvent реализует nats.SavedSearchMatcher.
func (a SavedSearchAdapter) MatchEvent(cameraID uuid.UUID, objectClass, color, kind string) []natspkg.SavedSearchMatch {
	if a.Service == nil {
		return nil
	}
	hits := a.Service.Match(EventFeatures{
		CameraID:    cameraID,
		ObjectClass: objectClass,
		Color:       color,
		Kind:        kind,
	})

	out := make([]natspkg.SavedSearchMatch, 0, len(hits))
	for _, hit := range hits {
		id, err := uuid.Parse(hit.ID)
		if err != nil {
			continue
		}
		out = append(out, natspkg.SavedSearchMatch{
			ID:        id,
			Name:      hit.Name,
			QueryText: hit.QueryText,
		})
	}
	return out
}

// MarkTriggered реализует nats.SavedSearchMatcher.
func (a SavedSearchAdapter) MarkTriggered(ctx context.Context, id uuid.UUID) {
	if a.Service != nil {
		a.Service.MarkTriggered(ctx, id)
	}
}
