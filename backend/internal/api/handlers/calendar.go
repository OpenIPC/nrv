package handlers

import (
	"net/http"
	"strconv"
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

	// Фильтр по камере: календарь показывает дни, когда писала эта камера.
	if raw := r.URL.Query().Get("camera_id"); raw != "" {
		id, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid camera_id"})
			return
		}
		query += ` AND r.camera_id = $4`
		args = append(args, id)
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

	writeJSON(w, http.StatusOK, CalendarResponse{
		Year:  year,
		Month: month,
		Days:  days,
	})
}

// DayTimeline отдаёт записи за конкретный день по времени.
//
// Отличается от обычного списка тем, что отдаёт только то, что нужно
// для отрисовки шкалы: начало, конец и тип. Ссылки на файлы и прочие
// поля не запрашиваются — на дне с сотнями клипов это лишние данные.
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

	// file_path нужен, чтобы шкала могла сразу воспроизвести клип:
	// обработчик File принимает именно путь, а не id записи.
	query := `
		SELECT r.id, r.camera_id, COALESCE(c.name, '') AS camera_name,
		       r.start_time, r.end_time,
		       COALESCE(r.trigger_type, '') AS trigger_type,
		       COALESCE(r.file_path, '') AS file_path
		FROM recordings r
		LEFT JOIN cameras c ON c.id = r.camera_id
		WHERE r.start_time >= $1 AND r.start_time < $2`

	args := []any{startLocal.UTC(), endLocal.UTC()}

	if raw := r.URL.Query().Get("camera_id"); raw != "" {
		id, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid camera_id"})
			return
		}
		query += ` AND r.camera_id = $3`
		args = append(args, id)
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
		ID          string  `json:"id"`
		CameraID    string  `json:"camera_id"`
		CameraName  string  `json:"camera_name"`
		StartTime   string  `json:"start_time"`
		EndTime     string  `json:"end_time"`
		TriggerType string  `json:"trigger_type"`
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
		startRatio := startTime.In(localZone()).Sub(startLocal).Seconds() / daySeconds
		endRatio := endTime.In(localZone()).Sub(startLocal).Seconds() / daySeconds

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

	writeJSON(w, http.StatusOK, map[string]any{
		"date":  dateStr,
		"items": items,
	})
}
