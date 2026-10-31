package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// CalendarDay описывает один день, в который есть записи.
type CalendarDay struct {
	// Дата в формате YYYY-MM-DD: так её удобно сравнивать строкой и
	// отдавать в интерфейс без разбора часовых поясов на стороне клиента.
	Date string `json:"date"`

	// Сколько клипов записано за этот день.
	Count int `json:"count"`

	// Суммарная длительность записей за день, в секундах.
	//
	// Нужна, чтобы оператор видел: в дне один короткий клип или запись
	// шла весь день. По одной только отметке «есть записи» это непонятно.
	Duration float64 `json:"duration"`

	// Типы событий, из-за которых велась запись. Показываются в интерфейсе,
	// чтобы можно было понять, что искать: движение, номер или тревогу.
	Triggers []string `json:"triggers"`

	// Сколько событий детекции попало в этот день с учётом фильтров.
	//
	// Отдельно от Count: клипов может быть много, а событий в них — ни
	// одного (постоянная запись), и наоборот. Оператору, который ищет
	// «где была машина», нужно второе число.
	Events int `json:"events"`
}

// CalendarResponse — ответ со списком дней за месяц.
type CalendarResponse struct {
	Year  int           `json:"year"`
	Month int           `json:"month"`
	Days  []CalendarDay `json:"days"`
}

// Calendar отдаёт дни месяца, в которые есть записи хотя бы одной камеры.
//
// Отдельный эндпоинт, а не фильтр в списке записей: интерфейсу нужно
// построить календарь сразу за месяц. Получать для этого список всех
// записей месяца и считать дни на клиенте — лишние десятки килобайт и
// заметная задержка при каждом перелистывании месяца.
//
// Запрос параметров:
//
//	year, month — месяц, который нужен календарю
//	camera_id   — необязательно, ограничить одной камерой
func (h *RecordingHandler) Calendar(w http.ResponseWriter, r *http.Request) {
	now := time.Now()

	year, err := strconv.Atoi(r.URL.Query().Get("year"))
	if err != nil || year < 2000 || year > 2100 {
		year = now.Year()
	}

	month, err := strconv.Atoi(r.URL.Query().Get("month"))
	// Месяц приходит в привычном виде (1–12), а не как в Go (0–11):
	// интерфейс отдаёт то, что видит пользователь, без пересчёта.
	if err != nil || month < 1 || month > 12 {
		month = int(now.Month())
	}

	// Границы месяца в UTC. Записи хранятся в UTC, поэтому и границы
	// берём в нём же — иначе дни на границе месяца сместились бы.
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	// Дата считается по часовому поясу сервера, а не UTC.
	//
	// Запись, сделанная в 23:30 по местному времени, в UTC попадает на
	// следующее число, и в календаре появилась бы не в тот день. Для
	// оператора важно местное время: он ищет событие «вчера вечером».
	query := `
		SELECT
			to_char(date_trunc('day', r.start_time AT TIME ZONE $3), 'YYYY-MM-DD') AS day,
			COUNT(*) AS cnt,
			COALESCE(SUM(EXTRACT(EPOCH FROM (r.end_time - r.start_time))), 0) AS dur,
			COALESCE(array_agg(DISTINCT r.trigger_type) FILTER (
				WHERE r.trigger_type IS NOT NULL AND r.trigger_type <> ''
			), '{}') AS triggers
		FROM recordings r
		WHERE r.start_time >= $1 AND r.start_time < $2`

	args := []any{start, end, localZoneName()}
	// Третий параметр (часовой пояс) уже занят, следующие — по порядку.
	argIdx := 4

	cameraIDs, camerasOK := parseCameraFilter(r)
	if !camerasOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid camera_id"})
		return
	}
	if len(cameraIDs) > 0 {
		query += ` AND r.camera_id = ANY($` + strconv.Itoa(argIdx) + `::uuid[])`
		args = append(args, cameraIDs)
		argIdx++
	}

	// Окно внутри суток: календарь должен считать по тем же правилам,
	// что и шкала дня, иначе число в ячейке не сойдётся с числом записей
	// на шкале, и оператор перестанет верить обоим.
	fromMinutes, toMinutes := 0, 24*60
	if raw := strings.TrimSpace(r.URL.Query().Get("from_time")); raw != "" {
		minutes, parsed := parseClockParam(raw)
		if !parsed {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid from_time"})
			return
		}
		fromMinutes = minutes
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to_time")); raw != "" {
		minutes, parsed := parseClockParam(raw)
		if !parsed {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid to_time"})
			return
		}
		toMinutes = minutes
	}
	if toMinutes <= fromMinutes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty time range"})
		return
	}
	// Сравниваем время окончания и начала записи с границами окна:
	// клип 07:40–08:30 относится и к окну «с 8 до 12».
	if fromMinutes > 0 {
		query += ` AND (r.end_time AT TIME ZONE $3)::time > $` + strconv.Itoa(argIdx) + `::time`
		args = append(args, clockText(fromMinutes))
		argIdx++
	}
	if toMinutes < 24*60 {
		query += ` AND (r.start_time AT TIME ZONE $3)::time < $` + strconv.Itoa(argIdx) + `::time`
		args = append(args, clockText(toMinutes))
		argIdx++
	}

	// Фильтры по событиям: день подходит, если внутри хотя бы одной его
	// записи есть событие нужного типа или с нужным объектом.
	search := parseArchiveSearch(r.URL.Query().Get("q"))
	classes := parseDetectionClasses(r)
	if search.Query != "" || len(classes) > 0 {
		condition := `EXISTS (
			SELECT 1 FROM detection_events e
			WHERE e.camera_id = r.camera_id
			  AND e.timestamp >= r.start_time AND e.timestamp < r.end_time`
		if len(classes) > 0 {
			condition += ` AND ` + detectionKindExpr("e.") + ` = ANY($` + strconv.Itoa(argIdx) + `)`
			args = append(args, classes)
			argIdx++
		}
		if searchCondition, searchArgs := search.where("e.", argIdx); searchCondition != "" {
			condition += searchCondition
			args = append(args, searchArgs...)
			argIdx += len(searchArgs)
		}
		condition += `)`
		query += ` AND ` + condition
	}

	query += ` GROUP BY day ORDER BY day`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		log.Error().Err(err).Msg("не удалось получить дни календаря")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	defer rows.Close()

	days := make([]CalendarDay, 0, 31)
	for rows.Next() {
		var d CalendarDay
		if scanErr := rows.Scan(&d.Date, &d.Count, &d.Duration, &d.Triggers); scanErr != nil {
			// Ошибку сканирования показываем в логе: без этого список
			// молча оказался бы короче, чем есть на самом деле.
			log.Error().Err(scanErr).Msg("не удалось прочитать день календаря")
			continue
		}
		if d.Triggers == nil {
			d.Triggers = []string{}
		}
		days = append(days, d)
	}

	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("ошибка чтения дней календаря")
	}

	// События по дням — отдельным запросом и с теми же фильтрами.
	//
	// В основном запросе их считать нельзя: соединение записей с событиями
	// размножило бы строки, и сумма длительности клипа посчиталась бы
	// столько раз, сколько в нём событий.
	eventsByDay, eventsErr := h.eventsByDay(r, start, end, cameraIDs, fromMinutes, toMinutes, classes, search)
	if eventsErr != nil {
		// Ошибка счётчика событий не должна убирать календарь: дни с
		// записями всё равно нужны.
		log.Error().Err(eventsErr).Msg("не удалось посчитать события по дням")
	}

	dayIndex := make(map[string]int, len(days))
	for i := range days {
		days[i].Events = eventsByDay[days[i].Date]
		dayIndex[days[i].Date] = i
	}
	// Дни, где есть только события без записей, тоже показываем: при
	// включённой детекции без записи оператору важно знать, что событие
	// было, даже если клипа за него нет.
	for day, count := range eventsByDay {
		if _, known := dayIndex[day]; known {
			continue
		}
		days = append(days, CalendarDay{Date: day, Count: 0, Duration: 0, Triggers: []string{}, Events: count})
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })

	writeJSON(w, http.StatusOK, CalendarResponse{
		Year:  year,
		Month: month,
		Days:  days,
	})
}

// eventsByDay считает события детекции по дням месяца с учётом фильтров.
func (h *RecordingHandler) eventsByDay(
	r *http.Request, start, end time.Time, cameraIDs []uuid.UUID,
	fromMinutes, toMinutes int, classes []string, search archiveSearch,
) (map[string]int, error) {
	query := `
		SELECT to_char(date_trunc('day', e.timestamp AT TIME ZONE $3), 'YYYY-MM-DD') AS day,
		       count(*)
		FROM detection_events e
		WHERE e.timestamp >= $1 AND e.timestamp < $2`
	args := []any{start, end, localZoneName()}
	argIdx := 4

	if len(cameraIDs) > 0 {
		query += ` AND e.camera_id = ANY($` + strconv.Itoa(argIdx) + `::uuid[])`
		args = append(args, cameraIDs)
		argIdx++
	}
	if fromMinutes > 0 {
		query += ` AND (e.timestamp AT TIME ZONE $3)::time >= $` + strconv.Itoa(argIdx) + `::time`
		args = append(args, clockText(fromMinutes))
		argIdx++
	}
	if toMinutes < 24*60 {
		query += ` AND (e.timestamp AT TIME ZONE $3)::time < $` + strconv.Itoa(argIdx) + `::time`
		args = append(args, clockText(toMinutes))
		argIdx++
	}
	if len(classes) > 0 {
		query += ` AND ` + detectionKindExpr("e.") + ` = ANY($` + strconv.Itoa(argIdx) + `)`
		args = append(args, classes)
		argIdx++
	}
	if condition, searchArgs := search.where("e.", argIdx); condition != "" {
		query += condition
		args = append(args, searchArgs...)
		argIdx += len(searchArgs)
	}
	query += ` GROUP BY day`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var day string
		var count int
		if scanErr := rows.Scan(&day, &count); scanErr != nil {
			log.Warn().Err(scanErr).Msg("не удалось прочитать счётчик событий дня")
			continue
		}
		counts[day] = count
	}
	return counts, rows.Err()
}

// parseCameraFilter собирает список камер из параметров camera_id и cameras.
//
// Оба варианта нужны: шкала по дорожкам ходит по одной камере (camera_id),
// а поиск и календарь — сразу по списку (cameras через запятую). Второй
// аргумент false означает неверный UUID — это ошибка запроса, а не
// «камер не выбрано».
func parseCameraFilter(r *http.Request) ([]uuid.UUID, bool) {
	ids := make([]uuid.UUID, 0, 4)

	appendID := func(raw string) bool {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return true
		}
		id, err := uuid.Parse(raw)
		if err != nil {
			return false
		}
		ids = append(ids, id)
		return true
	}

	if !appendID(r.URL.Query().Get("camera_id")) {
		return nil, false
	}
	for _, part := range strings.Split(r.URL.Query().Get("cameras"), ",") {
		if !appendID(part) {
			return nil, false
		}
	}
	// Пустой список — это nil, а не пустой срез: выше по коду nil означает
	// «без ограничений», иначе поиск решил бы, что камер не выбрано ни одной.
	if len(ids) == 0 {
		return nil, true
	}
	return ids, true
}

// parseDetectionClasses разбирает список типов детекций из detection_class.
func parseDetectionClasses(r *http.Request) []string {
	classes := make([]string, 0, 4)
	for _, part := range strings.Split(r.URL.Query().Get("detection_class"), ",") {
		if part = strings.TrimSpace(part); part != "" {
			classes = append(classes, part)
		}
	}
	return classes
}

// clockText переводит минуты от полуночи в «ЧЧ:ММ:СС» для SQL-параметра.
func clockText(minutes int) string {
	return fmt.Sprintf("%02d:%02d:00", minutes/60, minutes%60)
}

// detectionKindExpr возвращает SQL-выражение типа детекции.
//
// Тип события (plate, face, line, object) в базе не хранится: он выводится
// из класса объекта и метаданных. Держим выражение в одном месте, чтобы
// отметки на шкале, найденные записи и счётчики календаря считали одинаково.
// alias — префикс колонок («e.» или пусто).
func detectionKindExpr(alias string) string {
	return `
		CASE
			WHEN ` + alias + `object_class = 'plate' THEN 'plate'
			WHEN ` + alias + `object_class = 'face' THEN 'face'
			WHEN ` + alias + `metadata ? 'crossing' THEN 'line'
			ELSE 'object'
		END`
}

// DayTimeline отдаёт записи за конкретный день по времени.
//
// Отличается от обычного списка тем, что отдаёт только то, что нужно
// для отрисовки шкалы: начало, конец и тип. Ссылки на файлы и прочие
// поля не запрашиваются — на дне с сотнями клипов это лишние данные.
//
// Параметры запроса:
//
//	date             — день, выбранный оператором (YYYY-MM-DD)
//	camera_id        — одна камера
//	cameras          — несколько камер через запятую
//	from_time,to_time — окно внутри суток (HH:MM); пусто — весь день
//	detection_class  — типы детекций через запятую (plate,face,line,object)
//	q                — свободный запрос для поиска по объектам («машина», «А123»)
func (h *RecordingHandler) DayTimeline(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date is required"})
		return
	}

	// Разбираем дату в местном поясе: интерфейс присылает день, который
	// выбрал оператор, а не момент времени в UTC.
	day, err := time.ParseInLocation("2006-01-02", dateStr, localZone())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid date"})
		return
	}

	// Середина дня → сутки от полуночи до полуночи по местному времени.
	startLocal := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, localZone())
	endLocal := startLocal.AddDate(0, 0, 1)

	// Окно внутри суток: оператор ищет «с 8 до 12», а не весь день.
	//
	// Окно — это не фильтр по началу записи, а пересечение с ней: клип,
	// начавшийся в 07:40 и идущий до 08:30, тоже относится к утру, и
	// выкидывать его нельзя.
	windowStart, windowEnd := startLocal, endLocal
	if raw := strings.TrimSpace(r.URL.Query().Get("from_time")); raw != "" {
		minutes, ok := parseClockParam(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid from_time"})
			return
		}
		windowStart = startLocal.Add(time.Duration(minutes) * time.Minute)
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("to_time")); raw != "" {
		minutes, ok := parseClockParam(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid to_time"})
			return
		}
		if minutes >= 24*60 {
			windowEnd = endLocal
		} else {
			windowEnd = startLocal.Add(time.Duration(minutes) * time.Minute)
		}
	}
	if !windowEnd.After(windowStart) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty time range"})
		return
	}

	// Камеры для фильтра: одиночный camera_id (так ходит шкала по дорожкам)
	// и список cameras — так ходит поиск по архиву.
	cameraIDs, camerasOK := parseCameraFilter(r)
	if !camerasOK {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid camera_id"})
		return
	}

	// file_path нужен, чтобы шкала могла сразу воспроизвести клип:
	// обработчик File принимает именно путь, а не id записи.
	query := `
		SELECT r.id, r.camera_id, COALESCE(c.name, '') AS camera_name,
		       r.start_time, r.end_time,
		       COALESCE(r.trigger_type, '') AS trigger_type,
		       COALESCE(r.file_path, '') AS file_path
		FROM recordings r
		LEFT JOIN cameras c ON c.id = r.camera_id
		WHERE r.end_time > $1 AND r.start_time < $2`

	args := []any{windowStart.UTC(), windowEnd.UTC()}
	if len(cameraIDs) > 0 {
		query += ` AND r.camera_id = ANY($3::uuid[])`
		args = append(args, cameraIDs)
	}

	query += ` ORDER BY r.start_time`

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		log.Error().Err(err).Msg("не удалось получить записи дня")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	defer rows.Close()

	// Отдаём плоскую структуру, а не domain.Recording: интерфейсу нужны
	// только эти поля, и лишние ссылки на файлы его не интересуют.
	type TimelineItem struct {
		ID          string `json:"id"`
		CameraID    string `json:"camera_id"`
		CameraName  string `json:"camera_name"`
		StartTime   string `json:"start_time"`
		EndTime     string `json:"end_time"`
		TriggerType string `json:"trigger_type"`
		// FilePath — путь к файлу записи в хранилище. Без него шкала
		// не сможет воспроизвести клип: File принимает path, не id.
		FilePath string `json:"file_path"`
		// Позиция на сутках в долях (0..1) — по ней шкала рисует блок
		// без пересчёта времени на клиенте.
		StartRatio float64 `json:"start_ratio"`
		EndRatio   float64 `json:"end_ratio"`
	}

	daySeconds := endLocal.Sub(startLocal).Seconds()
	items := make([]TimelineItem, 0, 64)

	for rows.Next() {
		var (
			id, cameraID, cameraName, trigger, filePath string
			startTime, endTime                          time.Time
		)

		if scanErr := rows.Scan(&id, &cameraID, &cameraName, &startTime, &endTime, &trigger, &filePath); scanErr != nil {
			log.Error().Err(scanErr).Msg("не удалось прочитать запись дня")
			continue
		}

		// Доли считаем от местной полуночи: на шкале сутки идут от 00:00
		// по времени оператора.
		//
		// Клип обрезаем по выбранному окну: при фильтре «с 8 до 12»
		// полоса не должна вылезать за подсвеченный участок шкалы, иначе
		// непонятно, что именно попало в выборку.
		clipStart := startTime.In(localZone())
		if clipStart.Before(windowStart) {
			clipStart = windowStart
		}
		clipEnd := endTime.In(localZone())
		if clipEnd.After(windowEnd) {
			clipEnd = windowEnd
		}

		startRatio := clipStart.Sub(startLocal).Seconds() / daySeconds
		endRatio := clipEnd.Sub(startLocal).Seconds() / daySeconds

		// Ограничиваем отрезком суток: клип, начавшийся до полуночи,
		// иначе ушёл бы за левый край шкалы.
		if startRatio < 0 {
			startRatio = 0
		}
		if endRatio > 1 {
			endRatio = 1
		}

		items = append(items, TimelineItem{
			ID:          id,
			CameraID:    cameraID,
			CameraName:  cameraName,
			StartTime:   startTime.In(localZone()).Format(time.RFC3339),
			EndTime:     endTime.In(localZone()).Format(time.RFC3339),
			TriggerType: trigger,
			FilePath:    filePath,
			StartRatio:  startRatio,
			EndRatio:    endRatio,
		})
	}

	if err := rows.Err(); err != nil {
		log.Error().Err(err).Msg("ошибка чтения записей дня")
	}

	// Отметки детекций на шкале.
	//
	// Считаем их здесь же, а не отдельным запросом из интерфейса: шкала без
	// них бессмысленна (не видно, где искать событие), а второй запрос
	// пришлось бы согласовывать по времени с первым.
	//
	// События группируем по корзинам времени: за сутки их тысячи, и отдавать
	// каждое отдельной отметкой нельзя — браузер не отрисует столько
	// элементов. Корзина в минуту даёт на шкале отметку шириной 1/1440 суток,
	// которая видна при увеличении.
	marks := make([]map[string]any, 0, 128)
	bucketSeconds := 60.0
	// Фильтр по типам: оператору часто нужны только номера или только лица,
	// и подсветка по всем событиям сразу мешает.
	classFilter := parseDetectionClasses(r)

	// Тип события считаем здесь, а не отдаём класс объекта.
	//
	// В detection_events лежит КЛАСС обнаруженного (car, person, cat), а тип
	// детекции (номера, лица, пересечение линии) выводится из него и из
	// метаданных. Интерфейсу нужен именно тип: по классу «только пересечения»
	// не отфильтровать — у пересечения класс остаётся классовым, а признак
	// лежит в metadata.crossing.
	kindExpr := detectionKindExpr("")

	// Свободный запрос оператора («машина», «кот», «А123») разбирается в
	// archive_search.go. Одно и то же условие применяется и к отметкам,
	// и к списку найденных записей: иначе подсветка на шкале показывала бы
	// не то, что нашлось.
	search := parseArchiveSearch(r.URL.Query().Get("q"))

	markQuery := `
		SELECT camera_id, kind,
		       floor(extract(epoch FROM (timestamp - $1)) / $3)::int AS bucket,
		       count(*)
		FROM (
			SELECT camera_id, ` + kindExpr + ` AS kind, timestamp,
			       object_class, COALESCE(matched_name, '') AS matched_name, metadata
			FROM detection_events
			WHERE timestamp >= $1 AND timestamp < $2`
	markArgs := []any{windowStart.UTC(), windowEnd.UTC(), bucketSeconds}
	markArgIdx := 4
	if len(cameraIDs) > 0 {
		markQuery += ` AND camera_id = ANY($` + strconv.Itoa(markArgIdx) + `::uuid[])`
		markArgs = append(markArgs, cameraIDs)
		markArgIdx++
	}
	markQuery += `
		) AS day_events
		WHERE TRUE`
	if len(classFilter) > 0 {
		markQuery += ` AND kind = ANY($` + strconv.Itoa(markArgIdx) + `)`
		markArgs = append(markArgs, classFilter)
		markArgIdx++
	}
	if condition, searchArgs := search.where("", markArgIdx); condition != "" {
		markQuery += condition
		markArgs = append(markArgs, searchArgs...)
		markArgIdx += len(searchArgs)
	}
	markQuery += ` GROUP BY 1, 2, 3 ORDER BY 3 LIMIT 4000`

	markRows, markErr := h.db.Query(r.Context(), markQuery, markArgs...)
	if markErr != nil {
		// Ошибка отметок не должна ломать шкалу записей: без подсветки
		// архив всё равно остаётся рабочим.
		log.Error().Err(markErr).Msg("не удалось получить отметки детекций")
	} else {
		defer markRows.Close()
		for markRows.Next() {
			var (
				cameraID, class string
				bucketIndex     int
				count           int
			)
			if scanErr := markRows.Scan(&cameraID, &class, &bucketIndex, &count); scanErr != nil {
				log.Warn().Err(scanErr).Msg("не удалось прочитать отметку детекции")
				continue
			}
			startRatio := float64(bucketIndex) * bucketSeconds / daySeconds
			endRatio := startRatio + bucketSeconds/daySeconds
			if endRatio > 1 {
				endRatio = 1
			}
			marks = append(marks, map[string]any{
				"camera_id":   cameraID,
				"kind":        class,
				"count":       count,
				"start_ratio": startRatio,
				"end_ratio":   endRatio,
			})
		}
	}

	// Записи, внутри которых есть подходящие события, — это и есть
	// результат поиска по архиву.
	//
	// Считаем только при заданном запросе или фильтре типов: без фильтров
	// «подходит» любая запись с событиями, и список ничего не сообщает
	// оператору, зато требует соединения двух таблиц за сутки.
	type ArchiveMatch struct {
		RecordingID   string   `json:"recording_id"`
		CameraID      string   `json:"camera_id"`
		Count         int      `json:"count"`
		Kinds         []string `json:"kinds"`
		ObjectClasses []string `json:"object_classes"`
		FirstTime     string   `json:"first_time"`
		LastTime      string   `json:"last_time"`
	}

	matches := make([]ArchiveMatch, 0, 32)
	totalMatches := 0

	if search.Query != "" || len(classFilter) > 0 {
		matchQuery := `
			SELECT r.id, e.camera_id, count(*) AS cnt,
			       array_agg(DISTINCT e.kind) AS kinds,
			       array_agg(DISTINCT e.object_class) AS classes,
			       min(e.timestamp) AS first_ts, max(e.timestamp) AS last_ts
			FROM (
				SELECT camera_id, ` + kindExpr + ` AS kind, timestamp,
				       object_class, COALESCE(matched_name, '') AS matched_name, metadata
				FROM detection_events
				WHERE timestamp >= $1 AND timestamp < $2`
		matchArgs := []any{windowStart.UTC(), windowEnd.UTC()}
		matchArgIdx := 3
		if len(cameraIDs) > 0 {
			matchQuery += ` AND camera_id = ANY($` + strconv.Itoa(matchArgIdx) + `::uuid[])`
			matchArgs = append(matchArgs, cameraIDs)
			matchArgIdx++
		}
		matchQuery += `
			) e
			JOIN recordings r ON r.camera_id = e.camera_id
			     AND e.timestamp >= r.start_time AND e.timestamp < r.end_time
			WHERE TRUE`
		if len(classFilter) > 0 {
			matchQuery += ` AND e.kind = ANY($` + strconv.Itoa(matchArgIdx) + `)`
			matchArgs = append(matchArgs, classFilter)
			matchArgIdx++
		}
		if condition, searchArgs := search.where("e.", matchArgIdx); condition != "" {
			matchQuery += condition
			matchArgs = append(matchArgs, searchArgs...)
			matchArgIdx += len(searchArgs)
		}
		matchQuery += ` GROUP BY r.id, e.camera_id ORDER BY min(e.timestamp) LIMIT 500`

		matchRows, matchErr := h.db.Query(r.Context(), matchQuery, matchArgs...)
		if matchErr != nil {
			// Ошибка поиска не должна ломать шкалу: записи дня всё равно
			// нужны, а сообщение об ошибке покажет интерфейс.
			log.Error().Err(matchErr).Msg("не удалось выполнить поиск по архиву")
		} else {
			defer matchRows.Close()
			for matchRows.Next() {
				var (
					match      ArchiveMatch
					firstTS    time.Time
					lastTS     time.Time
					kinds      []string
					classes    []string
					matchCount int
				)
				if scanErr := matchRows.Scan(
					&match.RecordingID, &match.CameraID, &matchCount,
					&kinds, &classes, &firstTS, &lastTS,
				); scanErr != nil {
					log.Warn().Err(scanErr).Msg("не удалось прочитать результат поиска")
					continue
				}
				match.Count = matchCount
				match.Kinds = kinds
				match.ObjectClasses = classes
				match.FirstTime = firstTS.In(localZone()).Format(time.RFC3339)
				match.LastTime = lastTS.In(localZone()).Format(time.RFC3339)
				matches = append(matches, match)
				totalMatches += matchCount
			}
		}
	}

	// Границы окна в долях суток: по ним шкала затемняет время, которое
	// оператор исключил фильтром.
	writeJSON(w, http.StatusOK, map[string]any{
		"date":  dateStr,
		"items": items,
		"window": map[string]any{
			"from_ratio": windowStart.Sub(startLocal).Seconds() / daySeconds,
			"to_ratio":   windowEnd.Sub(startLocal).Seconds() / daySeconds,
		},
		"detections": marks,
		"matches":    matches,
		"search": map[string]any{
			"query":   search.Query,
			"classes": search.Classes,
			"ignored": search.Ignored,
			"events":  totalMatches,
		},
	})
}

// parseClockParam разбирает время суток в минуты от полуночи.
//
// Принимает «ЧЧ:ММ», «Ч:ММ» и «ЧЧ:ММ:СС»: форматы приходят из разных мест
// (поле ввода, закладка в адресе) и различаются мелочами, из-за которых
// фильтр молча переставал бы работать. «24:00» разрешено — это удобная
// запись конца суток.
func parseClockParam(raw string) (int, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}

	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 24 {
		return 0, false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, false
	}
	if hour == 24 && minute != 0 {
		return 0, false
	}
	if len(parts) == 3 {
		second, secErr := strconv.Atoi(parts[2])
		if secErr != nil || second < 0 || second > 59 {
			return 0, false
		}
	}

	return hour*60 + minute, true
}
