package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvr/backend/internal/domain"
	miniorepo "github.com/nvr/backend/internal/repository/minio"
	"github.com/nvr/backend/internal/service"
	"github.com/rs/zerolog/log"
)

type RecordingHandler struct {
	db *pgxpool.Pool
	// videoRepo может быть nil, если MinIO недоступен: тогда архив
	// остаётся доступен для чтения метаданных, но без ссылок на файлы.
	videoRepo *miniorepo.VideoRepo
	// storage нужен для отдачи файлов из локального хранилища.
	storage *service.StorageService
}

func NewRecordingHandler(db *pgxpool.Pool, videoRepo *miniorepo.VideoRepo, storage *service.StorageService) *RecordingHandler {
	return &RecordingHandler{db: db, videoRepo: videoRepo, storage: storage}
}

// signedURL возвращает ссылку на файл записи.
// Пустая строка означает, что ссылку получить не удалось.
//
// Ссылка ВСЕГДА относительная (на этот же хост, что и интерфейс), потому что
// presigned-ссылка MinIO подписана под конкретный Host и ломается при открытии
// архива с другого компьютера или через внешний IP. Бэкенд сам выступит прокси:
// объект из MinIO отдаёт обработчик File.
func (h *RecordingHandler) signedURL(filePath string) string {
	if filePath == "" {
		return ""
	}
	return "/api/v1/recordings/file?path=" + url.QueryEscape(filePath)
}

// File отдаёт файл записи — из MinIO или с локального диска.
// GET /api/v1/recordings/file?path=minio%3A... | local%3A%2F...
//
// Зарегистрирован вне JWT-группы: файл воспроизводится в теге <video>,
// который не передаёт заголовок Authorization. Токен проверяется в query.
//
// Отдача идёт через бэкенд (а не presigned-ссылкой MinIO), потому что подпись
// MinIO привязана к Host: при открытии интерфейса по внешнему адресу
// такая ссылка указывала бы на localhost клиента и не работала.
func (h *RecordingHandler) File(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is required"})
		return
	}

	// Скачивание отдаём с именем файла: браузер иначе сохранит объект
	// как «file» без расширения, и открыть его будет нечем.
	if r.URL.Query().Get("download") != "" {
		name := recordingFileName(r.URL.Query().Get("name"))
		w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	}

	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")

	if strings.HasPrefix(path, "local:") {
		full, err := service.LocalRecordingPath(path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
			return
		}
		// ServeFile сам разбирается с Range-запросами и отдаёт 206.
		http.ServeFile(w, r, full)
		return
	}

	if h.videoRepo == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "storage unavailable"})
		return
	}

	key := strings.TrimPrefix(path, "minio:")
	offset, length, err := parseRange(r.Header.Get("Range"))
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", length))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	obj, err := h.videoRepo.OpenRange(r.Context(), key, offset)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording file not found"})
		return
	}
	defer obj.Close()

	size := obj.Size
	if length > 0 && offset+length > size {
		length = size - offset
	}
	if length <= 0 {
		length = size - offset
	}

	if offset > 0 || length < size {
		// Частичный ответ: без него Chrome не начинает воспроизведение
		// и не даёт перематывать.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, size))
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	// Стримим без буферизации в память: клипы бывают по 20+ МБ.
	io.CopyN(w, obj, length)
}

// recordingFileName готовит безопасное имя файла для скачивания.
//
// Имя приходит из интерфейса (камера + время), поэтому в нём нельзя
// доверять ничему: кавычки и перевод строки сломали бы заголовок
// Content-Disposition, а разделители пути — позволили бы записать
// файл вне папки загрузок.
func recordingFileName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "recording.mp4"
	}

	// Оставляем буквы, цифры и безопасную пунктуацию. Кириллицу сохраняем:
	// имена камер у нас русские, и вырезать их было бы неудобно.
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'а' && r <= 'я', r >= 'А' && r <= 'Я', r == 'ё', r == 'Ё':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}

	name := strings.TrimSpace(b.String())
	if name == "" {
		return "recording.mp4"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".mp4") {
		name += ".mp4"
	}
	return name
}

// parseRange разбирает заголовок Range вида "bytes=0-1023".
// Возвращает смещение и длину. Если заголовка нет — offset=0, length=0
// (это означает «отдать файл целиком»).
func parseRange(header string) (offset, length int64, err error) {
	if header == "" {
		return 0, 0, nil
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok {
		return 0, 0, nil // неизвестный формат — отдаём файл целиком
	}
	spec = strings.TrimSpace(strings.Split(spec, ",")[0])
	startStr, endStr, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, fmt.Errorf("invalid range")
	}

	if startStr == "" {
		// Форма "bytes=-500": последние 500 байт — для видео не нужна,
		// отдаём файл целиком.
		return 0, 0, nil
	}
	offset, err = strconv.ParseInt(startStr, 10, 64)
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("invalid range start")
	}
	if endStr == "" {
		return offset, 0, nil // "bytes=500-" — до конца файла
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil || end < offset {
		return 0, 0, fmt.Errorf("invalid range end")
	}
	return offset, end - offset + 1, nil
}

func (h *RecordingHandler) List(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	cameraIDStr := r.URL.Query().Get("camera_id")
	var cameraID *uuid.UUID
	if cameraIDStr != "" {
		id, err := uuid.Parse(cameraIDStr)
		if err == nil {
			cameraID = &id
		}
	}

	// resolution и codec могут быть NULL — приводим к пустой строке,
	// иначе rows.Scan не сможет записать NULL в string и запись молча теряется.
	// JOIN с cameras нужен, чтобы в архиве показывать имя камеры, а не UUID.
	query := `SELECT r.id, r.camera_id, COALESCE(c.name,'') AS camera_name,
		r.start_time, r.end_time,
		EXTRACT(EPOCH FROM (r.end_time - r.start_time))::float as duration,
		r.file_path, r.file_size, COALESCE(r.resolution,'') AS resolution,
		COALESCE(r.codec,'') AS codec, r.event_triggered, COALESCE(r.metadata,'{}') AS metadata,
		COALESCE(r.trigger_type,'') AS trigger_type, COALESCE(r.trigger_detail,'') AS trigger_detail
		FROM recordings r LEFT JOIN cameras c ON c.id = r.camera_id WHERE 1=1`
	countQuery := `SELECT COUNT(*) FROM recordings WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if cameraID != nil {
		query += ` AND camera_id = $` + strconv.Itoa(argIdx)
		countQuery += ` AND camera_id = $` + strconv.Itoa(argIdx)
		args = append(args, cameraID.String())
		argIdx++
	}

	// Фильтр по причине записи — основной способ найти нужное в архиве:
	// «покажи всё, что записалось из-за номеров».
	if trigger := r.URL.Query().Get("trigger"); trigger != "" {
		query += ` AND r.trigger_type = $` + strconv.Itoa(argIdx)
		countQuery += ` AND trigger_type = $` + strconv.Itoa(argIdx)
		args = append(args, trigger)
		argIdx++
	}

	// Поиск по расшифровке триггера: номер машины, имя человека, класс объекта.
	if search := strings.TrimSpace(r.URL.Query().Get("search")); search != "" {
		cond := ` AND r.trigger_detail ILIKE $` + strconv.Itoa(argIdx)
		query += cond
		countQuery += ` AND trigger_detail ILIKE $` + strconv.Itoa(argIdx)
		args = append(args, "%"+search+"%")
		argIdx++
	}

	// Total count
	var total int64
	if err := h.db.QueryRow(r.Context(), countQuery, args...).Scan(&total); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	offset := (page - 1) * pageSize
	query += ` ORDER BY r.start_time DESC LIMIT $` + strconv.Itoa(argIdx) + ` OFFSET $` + strconv.Itoa(argIdx+1)
	args = append(args, pageSize, offset)

	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer rows.Close()

	recordings := make([]domain.Recording, 0)
	// Ссылки на файлы формируем отдельно: они не входят в domain.Recording,
	// но нужны фронтенду для воспроизведения.
	urls := make([]string, 0, 20)
	for rows.Next() {
		var rec domain.Recording
		if err := rows.Scan(
			&rec.ID, &rec.CameraID, &rec.CameraName, &rec.StartTime, &rec.EndTime,
			&rec.Duration, &rec.FilePath, &rec.FileSize,
			&rec.Resolution, &rec.Codec, &rec.EventTriggered, &rec.Metadata,
			&rec.TriggerType, &rec.TriggerDetail,
		); err != nil {
			continue
		}
		recordings = append(recordings, rec)
		urls = append(urls, h.signedURL(rec.FilePath))
	}

	// Подписи ссылок генерируются параллельно, поэтому собираем ответ вручную.
	items := make([]map[string]any, 0, len(recordings))
	for i, rec := range recordings {
		items = append(items, recordingResponse(rec, urls[i]))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recordings": items,
		"total":      total,
		"page":       page,
		"page_size":  pageSize,
	})
}

func (h *RecordingHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var rec domain.Recording
	err = h.db.QueryRow(r.Context(),
		`SELECT r.id, r.camera_id, COALESCE(c.name,'') AS camera_name,
		r.start_time, r.end_time,
		EXTRACT(EPOCH FROM (r.end_time - r.start_time))::float as duration,
		r.file_path, r.file_size, COALESCE(r.resolution,'') AS resolution,
		COALESCE(r.codec,'') AS codec, r.event_triggered, COALESCE(r.metadata,'{}') AS metadata,
		COALESCE(r.trigger_type,'') AS trigger_type, COALESCE(r.trigger_detail,'') AS trigger_detail
		FROM recordings r LEFT JOIN cameras c ON c.id = r.camera_id WHERE r.id = $1`, id.String(),
	).Scan(
		&rec.ID, &rec.CameraID, &rec.CameraName, &rec.StartTime, &rec.EndTime,
		&rec.Duration, &rec.FilePath, &rec.FileSize,
		&rec.Resolution, &rec.Codec, &rec.EventTriggered, &rec.Metadata,
		&rec.TriggerType, &rec.TriggerDetail,
	)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording not found"})
		return
	}

	// Ссылка на файл в хранилище (действует час).
	writeJSON(w, http.StatusOK, recordingResponse(rec, h.signedURL(rec.FilePath)))
}

// recordingResponse добавляет к записи временную ссылку на файл.
func recordingResponse(rec domain.Recording, url string) map[string]any {
	return map[string]any{
		"id":              rec.ID,
		"camera_id":       rec.CameraID,
		"camera_name":     rec.CameraName,
		"start_time":      rec.StartTime,
		"end_time":        rec.EndTime,
		"duration":        rec.Duration,
		"file_path":       rec.FilePath,
		"file_size":       rec.FileSize,
		"resolution":      rec.Resolution,
		"codec":           rec.Codec,
		"event_triggered": rec.EventTriggered,
		"trigger_type":    rec.TriggerType,
		"trigger_detail":  rec.TriggerDetail,
		"metadata":        rec.Metadata,
		"url":             url,
	}
}

func (h *RecordingHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	// Сначала забираем ключ объекта, чтобы удалить и файл, и запись.
	var filePath string
	err = h.db.QueryRow(r.Context(),
		`SELECT COALESCE(file_path, '') FROM recordings WHERE id = $1`, id.String(),
	).Scan(&filePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "recording not found"})
		return
	}

	if _, err := h.db.Exec(r.Context(), `DELETE FROM recordings WHERE id = $1`, id.String()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Файл удаляем после записи: если хранилище недоступно, метаданные
	// уже удалены и «осиротевший» объект можно убрать отдельной задачей.
	if h.videoRepo != nil && filePath != "" {
		if err := h.videoRepo.Delete(r.Context(), filePath); err != nil {
			log.Warn().Err(err).Str("key", filePath).Msg("failed to delete recording file from storage")
		}
	}

	writeJSON(w, http.StatusNoContent, nil)
}

// Используемый time для ответа stats
var _ = time.Now
