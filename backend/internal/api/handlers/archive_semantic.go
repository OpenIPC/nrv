package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/vector"
	"github.com/rs/zerolog/log"
)

// TextEmbedder превращает текстовый запрос в вектор.
//
// Интерфейс, а не конкретный клиент: обработчик не должен зависеть от
// способа доставки запроса в модель (сейчас это NATS до контейнера
// детектора), а тесты обходятся заглушкой.
type TextEmbedder interface {
	// EmbedText возвращает вектор запроса, имя модели и её параметры
	// калибровки (масштаб и сдвиг логита). Без них косинус нельзя перевести
	// в вероятность: у SigLIP шкала косинуса на порядок меньше, чем у CLIP.
	EmbedText(ctx context.Context, text string) ([]float32, string, float64, float64, error)
}

// ArchiveSearchHandler ищет записи по описанию: «красная машина», «человек
// с собакой», «фургон у калитки».
//
// Работает поверх векторов событий: детектор считает CLIP-вектор кадра,
// бэкенд считает вектор запроса той же моделью и сравнивает их косинусом.
// Сравнение идёт в памяти по событиям выбранного окна — для нашего объёма
// (сотни событий в сутки) это быстрее и проще, чем векторный индекс в базе.
type ArchiveSearchHandler struct {
	db       *pgxpool.Pool
	embedder TextEmbedder
	// guard — что видно тому, кто пришёл: поиск по архиву фильтруется по
	// доступу к камере так же, как список камер и шкала дня.
	guard *AccessGuard
}

// NewArchiveSearchHandler создаёт обработчик поиска по смыслу.
func NewArchiveSearchHandler(db *pgxpool.Pool, embedder TextEmbedder) *ArchiveSearchHandler {
	return &ArchiveSearchHandler{db: db, embedder: embedder}
}

// WithGuard подключает проверку доступа к камерам.
func (h *ArchiveSearchHandler) WithGuard(guard *AccessGuard) *ArchiveSearchHandler {
	h.guard = guard
	return h
}

// SemanticResult — одно найденное событие.
type SemanticResult struct {
	EventID    string `json:"event_id"`
	CameraID   string `json:"camera_id"`
	CameraName string `json:"camera_name"`
	// RecordingID — запись, в которой лежит это событие. Пусто, если клипа
	// за событие нет: детекция была, а запись не велась.
	RecordingID string `json:"recording_id,omitempty"`
	Timestamp   string `json:"timestamp"`
	ObjectClass string `json:"object_class"`
	// Color — цвет объекта из признаков, посчитанных детектором.
	Color string `json:"color,omitempty"`
	// Score — косинусное сходство с запросом. Ноль означает, что событие
	// нашлось по словам (класс, номер, цвет), а не по картинке.
	Score float32 `json:"score"`
	// Lexical — событие совпало по точным признакам.
	Lexical      bool   `json:"lexical,omitempty"`
	SnapshotPath string `json:"snapshot_path,omitempty"`
}

// Search ищет события по текстовому описанию.
//
// Параметры:
//
//	q                — что искать («красная машина», «человек с сумкой»)
//	date             — день (YYYY-MM-DD), по умолчанию сегодняшний
//	from_time/to_time — окно внутри суток
//	camera_id/cameras — ограничение камерами
//	limit            — сколько результатов вернуть (по умолчанию 40)
func (h *ArchiveSearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	// similar_to — поиск «похожих» на уже найденное событие. Тогда текст
	// не нужен: вектором запроса служит вектор самого события.
	similarTo := strings.TrimSpace(r.URL.Query().Get("similar_to"))
	if query == "" && similarTo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q is required"})
		return
	}
	if h.embedder == nil && similarTo == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "semantic search is disabled",
		})
		return
	}

	// Окно поиска: день и, при необходимости, часы внутри него.
	day := strings.TrimSpace(r.URL.Query().Get("date"))
	if day == "" {
		day = time.Now().In(localZone()).Format("2006-01-02")
	}
	parsedDay, err := time.ParseInLocation("2006-01-02", day, localZone())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid date"})
		return
	}

	windowStart := time.Date(parsedDay.Year(), parsedDay.Month(), parsedDay.Day(), 0, 0, 0, 0, localZone())
	windowEnd := windowStart.AddDate(0, 0, 1)
	if raw := strings.TrimSpace(r.URL.Query().Get("from_time")); raw != "" {
		minutes, ok := parseClockParam(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid from_time"})
			return
		}
		windowStart = windowStart.Add(time.Duration(minutes) * time.Minute)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to_time")); raw != "" {
		minutes, ok := parseClockParam(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid to_time"})
			return
		}
		if minutes < 24*60 {
			windowEnd = time.Date(parsedDay.Year(), parsedDay.Month(), parsedDay.Day(), 0, 0, 0, 0, localZone()).
				Add(time.Duration(minutes) * time.Minute)
		}
	}
	if !windowEnd.After(windowStart) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty time range"})
		return
	}

	limit := 40
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}

	// Камеры: то, что просил оператор, пересечённое с тем, что ему можно.
	requested, camerasOK := parseCameraFilter(r)
	if !camerasOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid camera_id"})
		return
	}
	cameraFilter := intersectCameras(requested, allowedCameraFilter(r, h.guard))
	if cameraFilter != nil && len(cameraFilter) == 0 {
		// Доступ есть только к камерам, которых нет в запросе.
		writeJSON(w, http.StatusOK, map[string]any{
			"query": query, "results": []SemanticResult{}, "total": 0,
		})
		return
	}

	// Вектор запроса: либо модель считает его по тексту, либо берём уже
	// посчитанный вектор события-образца (поиск «похожих»).
	var (
		queryVector []float32
		model       string
		// Параметры калибровки: score = sigmoid(scale*cos + bias).
		// Нули означают «модель их не отдаёт» — тогда работаем с косинусом.
		modelScale float64
		modelBias  float64
	)
	if similarTo != "" {
		queryVector, model, err = h.vectorForEvent(r.Context(), similarTo, cameraFilter)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
			return
		}
	} else {
		vector, name, scale, bias, embedErr := h.embedder.EmbedText(r.Context(), query)
		err = embedErr
		if err != nil {
			log.Warn().Err(err).Str("query", query).Msg("модель поиска по смыслу недоступна")
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "semantic model is not ready",
			})
			return
		}
		queryVector, model = vector, name
		modelScale, modelBias = scale, bias
	}

	candidates, err := h.candidates(r.Context(), windowStart, windowEnd, cameraFilter, model)
	if err != nil {
		log.Error().Err(err).Msg("не удалось прочитать векторы событий")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}

	// Разбор запроса нужен до сравнения векторов: если оператор назвал цвет,
	// векторные результаты другого цвета отбрасываются. CLIP на вопрос «какой
	// цвет» отвечает приблизительно, а metadata — точно.
	search := parseArchiveSearch(query)

	// Оценка сходства. Если модель отдала и масштаб, и сдвиг логита, считаем
	// вероятность: sigmoid(scale*cos + bias). Так обучен SigLIP, и только так
	// его маленькие косинусы (0.05 — уже уверенное совпадение) ложатся в
	// осмысленный порог. У CLIP сдвига нет: он обучался softmax-лоссом, и
	// sigmoid там давал бы единицу на любом кадре — поэтому признак
	// калибровки именно сдвиг, а не масштаб.
	calibrated := modelBias != 0
	minScore := defaultMinScore(model, calibrated)
	if raw := r.URL.Query().Get("min_score"); raw != "" {
		if parsed, parseErr := strconv.ParseFloat(raw, 32); parseErr == nil && parsed >= 0 && parsed <= 1 {
			minScore = float32(parsed)
		}
	}

	vectorHits := make([]SemanticResult, 0, 64)
	for _, candidate := range candidates {
		// Названный класс и цвет — жёсткие условия и для векторов: «красная
		// машина» не должна выдавать красного человека, даже если CLIP
		// считает кадр похожим.
		if len(search.Classes) > 0 && !containsValue(search.Classes, candidate.objectClass) {
			continue
		}
		if len(search.Colors) > 0 && !containsValue(search.Colors, candidate.color) {
			continue
		}
		cos := vector.Cosine(queryVector, candidate.vector)
		score := cos
		if calibrated {
			score = float32(sigmoid(modelScale*float64(cos) + modelBias))
		}
		if score < minScore {
			continue
		}
		vectorHits = append(vectorHits, SemanticResult{
			EventID:      candidate.eventID,
			CameraID:     candidate.cameraID,
			CameraName:   candidate.cameraName,
			Timestamp:    candidate.timestamp.In(localZone()).Format(time.RFC3339),
			ObjectClass:  candidate.objectClass,
			Color:        candidate.color,
			Score:        score,
			SnapshotPath: candidate.snapshotPath,
		})
	}
	sort.SliceStable(vectorHits, func(i, j int) bool { return vectorHits[i].Score > vectorHits[j].Score })

	// Точная выдача: класс, цвет, номер, имя из справочника. Без неё поиск
	// теряет самое надёжное: CLIP не умеет отвечать «это точно машина»,
	// а по классу и цвету ответ однозначный.
	var lexicalHits []SemanticResult
	if search.Query != "" {
		hits, lexErr := h.lexicalCandidates(r.Context(), windowStart, windowEnd, cameraFilter, search, limit)
		if lexErr != nil {
			// Ошибка точного поиска не должна убирать векторную выдачу.
			log.Warn().Err(lexErr).Msg("не удалось выполнить поиск по признакам")
		} else {
			lexicalHits = hits
		}
	}

	results := fuseResults(vectorHits, lexicalHits, limit)
	h.attachRecordings(r.Context(), results)

	writeJSON(w, http.StatusOK, map[string]any{
		"query":   query,
		"model":   model,
		"total":   len(results),
		"results": results,
	})
}

// fuseResults объединяет векторную и точную выдачу.
//
// Recriprocal Rank Fusion: итоговый вес события — сумма 1/(k + место) по
// тем спискам, где оно встретилось. Простое и устойчивое решение: не нужно
// приводить косинус и совпадение по классу к одной шкале (они несравнимы),
// а событие, найденное обоими способами, честно поднимается наверх.
func fuseResults(vectorHits, lexicalHits []SemanticResult, limit int) []SemanticResult {
	const rrfK = 60.0

	merged := make(map[string]*SemanticResult, len(vectorHits)+len(lexicalHits))
	weights := make(map[string]float64, len(vectorHits)+len(lexicalHits))

	add := func(hits []SemanticResult) {
		for rank, hit := range hits {
			entry, known := merged[hit.EventID]
			if !known {
				item := hit
				merged[hit.EventID] = &item
			} else {
				// Событие нашлось двумя способами: сохраняем более полные
				// данные, а не первый попавшийся набор.
				if entry.SnapshotPath == "" {
					entry.SnapshotPath = hit.SnapshotPath
				}
				if entry.Color == "" {
					entry.Color = hit.Color
				}
				if hit.Score > entry.Score {
					entry.Score = hit.Score
				}
				entry.Lexical = entry.Lexical || hit.Lexical
			}
			weights[hit.EventID] += 1.0 / (rrfK + float64(rank+1))
		}
	}
	add(vectorHits)
	add(lexicalHits)

	ids := make([]string, 0, len(merged))
	for id := range merged {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if weights[ids[i]] != weights[ids[j]] {
			return weights[ids[i]] > weights[ids[j]]
		}
		// При равном весе — более свежее событие выше.
		return merged[ids[i]].Timestamp > merged[ids[j]].Timestamp
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}

	results := make([]SemanticResult, 0, len(ids))
	for _, id := range ids {
		results = append(results, *merged[id])
	}
	return results
}

// semanticCandidate — событие с вектором, ещё без оценки.
type semanticCandidate struct {
	eventID      string
	cameraID     string
	cameraName   string
	timestamp    time.Time
	objectClass  string
	snapshotPath string
	color        string
	vector       []float32
}

// candidates читает события окна вместе с их векторами.
//
// Ограничение по модели обязательно: после смены модели в базе окажутся
// векторы разных пространств, и сравнение их между собой дало бы случайные
// числа, неотличимые от результата.
func (h *ArchiveSearchHandler) candidates(
	ctx context.Context, from, to time.Time, cameras []uuid.UUID, model string,
) ([]semanticCandidate, error) {
	query := `
		SELECT e.id, e.camera_id, COALESCE(c.name, ''), e.timestamp,
		       e.object_class, COALESCE(e.snapshot_path, ''),
		       COALESCE(e.metadata->>'color', ''), em.vector
		FROM detection_events e
		JOIN event_embeddings em ON em.event_id = e.id
		LEFT JOIN cameras c ON c.id = e.camera_id
		WHERE e.timestamp >= $1 AND e.timestamp < $2 AND em.model = $3`
	args := []any{from.UTC(), to.UTC(), model}

	if cameras != nil {
		query += ` AND e.camera_id = ANY($4::uuid[])`
		args = append(args, cameras)
	}
	// Порядок по времени, а не по сходству: сходство считаем в Go, а
	// ограничение защищает память от запроса за месяц по всем камерам.
	query += ` ORDER BY e.timestamp LIMIT 50000`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := make([]semanticCandidate, 0, 256)
	for rows.Next() {
		var (
			candidate semanticCandidate
			raw       []byte
		)
		if scanErr := rows.Scan(&candidate.eventID, &candidate.cameraID, &candidate.cameraName,
			&candidate.timestamp, &candidate.objectClass, &candidate.snapshotPath,
			&candidate.color, &raw); scanErr != nil {
			log.Warn().Err(scanErr).Msg("не удалось прочитать вектор события")
			continue
		}
		candidate.vector = vector.Decode(raw)
		if len(candidate.vector) == 0 {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// attachRecordings проставляет ссылку на запись для найденных событий.
//
// Один запрос на все результаты, а не по одному на каждое событие: иначе
// сорок результатов — сорок обращений к базе.
func (h *ArchiveSearchHandler) attachRecordings(ctx context.Context, results []SemanticResult) {
	if len(results) == 0 {
		return
	}

	ids := make([]uuid.UUID, 0, len(results))
	for _, result := range results {
		if id, err := uuid.Parse(result.EventID); err == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}

	rows, err := h.db.Query(ctx, `
		SELECT e.id, r.id
		FROM detection_events e
		JOIN recordings r ON r.camera_id = e.camera_id
		     AND e.timestamp >= r.start_time AND e.timestamp < r.end_time
		WHERE e.id = ANY($1::uuid[])`, ids)
	if err != nil {
		// Без ссылки на клип результат всё равно полезен: снимок события
		// есть, и оператор увидит кадр.
		log.Warn().Err(err).Msg("не удалось сопоставить события с записями")
		return
	}
	defer rows.Close()

	byEvent := make(map[string]string, len(results))
	for rows.Next() {
		var eventID, recordingID string
		if scanErr := rows.Scan(&eventID, &recordingID); scanErr == nil {
			byEvent[eventID] = recordingID
		}
	}
	for i := range results {
		results[i].RecordingID = byEvent[results[i].EventID]
	}
}

// vectorForEvent достаёт сохранённый вектор события-образца.
//
// Нужен для кнопки «похожие»: оператор нашёл один кадр и хочет увидеть
// остальные такие же. Вектор уже посчитан, поэтому модель не нужна вовсе —
// поиск работает даже когда она занята.
func (h *ArchiveSearchHandler) vectorForEvent(
	ctx context.Context, eventID string, cameras []uuid.UUID,
) ([]float32, string, error) {
	id, err := uuid.Parse(eventID)
	if err != nil {
		return nil, "", err
	}

	query := `
		SELECT em.model, em.vector
		FROM event_embeddings em
		JOIN detection_events e ON e.id = em.event_id
		WHERE em.event_id = $1`
	args := []any{id}
	// Если событие-образец принадлежит чужой камере, ссылка не должна
	// открывать по нему весь архив.
	if cameras != nil {
		query += ` AND e.camera_id = ANY($2::uuid[])`
		args = append(args, cameras)
	}

	var (
		model string
		raw   []byte
	)
	if scanErr := h.db.QueryRow(ctx, query, args...).Scan(&model, &raw); scanErr != nil {
		return nil, "", scanErr
	}

	decoded := vector.Decode(raw)
	if len(decoded) == 0 {
		return nil, "", errors.New("empty vector")
	}
	return decoded, model, nil
}

// lexicalCandidates ищет события по точным признакам: класс, цвет, номер,
// имя из справочника.
//
// Порядок — от свежих к старым: для точного совпадения «когда» важнее,
// чем «насколько», а векторная часть выдачи всё равно добавляет свою
// оценку сходства.
func (h *ArchiveSearchHandler) lexicalCandidates(
	ctx context.Context, from, to time.Time, cameras []uuid.UUID,
	search archiveSearch, limit int,
) ([]SemanticResult, error) {
	if search.Query == "" {
		return nil, nil
	}

	query := `
		SELECT e.id, e.camera_id, COALESCE(c.name, ''), e.timestamp,
		       e.object_class, COALESCE(e.snapshot_path, ''),
		       COALESCE(e.metadata->>'color', '')
		FROM detection_events e
		LEFT JOIN cameras c ON c.id = e.camera_id
		WHERE e.timestamp >= $1 AND e.timestamp < $2`
	args := []any{from.UTC(), to.UTC()}
	nextArg := 3

	if cameras != nil {
		query += ` AND e.camera_id = ANY($` + strconv.Itoa(nextArg) + `::uuid[])`
		args = append(args, cameras)
		nextArg++
	}
	if condition, searchArgs := search.where("e.", nextArg); condition != "" {
		query += condition
		args = append(args, searchArgs...)
		nextArg += len(searchArgs)
	}
	query += ` ORDER BY e.timestamp DESC LIMIT ` + strconv.Itoa(limit*3)

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hits := make([]SemanticResult, 0, limit)
	for rows.Next() {
		var (
			hit       SemanticResult
			timestamp time.Time
		)
		if scanErr := rows.Scan(&hit.EventID, &hit.CameraID, &hit.CameraName, &timestamp,
			&hit.ObjectClass, &hit.SnapshotPath, &hit.Color); scanErr != nil {
			continue
		}
		hit.Timestamp = timestamp.In(localZone()).Format(time.RFC3339)
		hit.Lexical = true
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

// sigmoid переводит логит в вероятность.
func sigmoid(x float64) float64 {
	return 1 / (1 + math.Exp(-x))
}

// defaultMinScore — порог по умолчанию для конкретной модели.
//
// Калиброванный случай (модель отдала масштаб логита): 0.5 — «модель
// считает, что подходит чаще, чем нет».
//
// Некалиброванный — это поиск «похожих» по сохранённому вектору: там
// сравниваются две картинки, и работаем с косинусом. У SigLIP шкала
// косинуса та же, что у текста (маленькая), у CLIP — обычная.
func defaultMinScore(model string, calibrated bool) float32 {
	if calibrated {
		return 0.5
	}
	if strings.Contains(strings.ToLower(model), "siglip") {
		return 0.5
	}
	return 0.2
}

// containsValue сообщает, есть ли значение в списке.
func containsValue(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// intersectCameras пересекает запрошенные камеры с разрешёнными.
//
// nil в любом из аргументов означает «без ограничений». Пустой непустой
// срез — «ни одной»: если запрошены только чужие камеры, результат пуст,
// и подменять его на «все» нельзя.
func intersectCameras(requested, allowed []uuid.UUID) []uuid.UUID {
	if allowed == nil {
		return requested
	}
	if requested == nil {
		return allowed
	}

	allowedSet := make(map[uuid.UUID]bool, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = true
	}
	result := make([]uuid.UUID, 0, len(requested))
	for _, id := range requested {
		if allowedSet[id] {
			result = append(result, id)
		}
	}
	return result
}
