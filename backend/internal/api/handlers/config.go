package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/nvr/backend/internal/configtransfer"
)

// maxConfigUpload — предел размера файла настроек.
//
// Настройки — это текст: даже большой сервер укладывается в единицы
// мегабайт. Предел нужен на случай, когда файлом выбрали что-то другое —
// видео или образ, — чтобы это не заняло память целиком.
const maxConfigUpload = 32 << 20

// ConfigHandler переносит настройки сервера между установками.
type ConfigHandler struct {
	db *pgxpool.Pool
	// appVersion попадает в файл для справки: по ней видно, сервер какой
	// версии снял настройки.
	appVersion string
	// apply вызывается после успешного импорта.
	//
	// Часть настроек живёт не только в базе: конфигурация Asterisk
	// собирается в файлы, а пути потоков регистрируются в медиасервере.
	// Без повторного применения сервер продолжил бы работать по прежним
	// настройкам, и перенос выглядел бы неудавшимся.
	apply func(ctx context.Context) error
}

// NewConfigHandler собирает обработчик переноса настроек. apply может быть
// nil — тогда после импорта ничего не переприменяется.
func NewConfigHandler(db *pgxpool.Pool, appVersion string, apply func(ctx context.Context) error) *ConfigHandler {
	return &ConfigHandler{db: db, appVersion: appVersion, apply: apply}
}

// Export отдаёт файл настроек для переноса на другой сервер.
func (h *ConfigHandler) Export(w http.ResponseWriter, r *http.Request) {
	bundle, err := configtransfer.Export(r.Context(), h.db, h.appVersion)
	if err != nil {
		log.Error().Err(err).Msg("не удалось выгрузить настройки")
		writeError(w, http.StatusInternalServerError, "не удалось выгрузить настройки: "+err.Error())
		return
	}

	// Отступы в файле нужны не красоты ради: такой файл человек открывает
	// глазами, когда перенос не удался, и должен уметь его прочитать.
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "не удалось собрать файл настроек: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="nvr-config-%s.json"`, time.Now().Format("2006-01-02-1504")))
	_, _ = w.Write(data)
}

// Import применяет файл настроек, полученный с другого сервера.
//
// С параметром preview=1 файл только проверяется: отчёт показывает, что
// получится, а база остаётся прежней.
func (h *ConfigHandler) Import(w http.ResponseWriter, r *http.Request) {
	preview := r.URL.Query().Get("preview") == "1"

	data, err := io.ReadAll(io.LimitReader(r.Body, maxConfigUpload+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "не удалось прочитать файл: "+err.Error())
		return
	}
	if len(data) > maxConfigUpload {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("файл больше %d МБ — это не файл настроек", maxConfigUpload>>20))
		return
	}

	report, err := configtransfer.Import(r.Context(), h.db, data, configtransfer.Options{Preview: preview})
	if err != nil {
		// Ошибки здесь — про содержимое файла и конфликты с уже заведёнными
		// записями, а не про нашу неисправность: отвечаем 400, чтобы
		// интерфейс показал человеку сам текст.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !preview && h.apply != nil {
		if err := h.apply(r.Context()); err != nil {
			// Отменять импорт поздно: настройки уже в базе. Но и молчать
			// нельзя — иначе домофония будет работать по прежним данным,
			// и причину будут искать не там.
			log.Error().Err(err).Msg("настройки перенесены, но не применены к службам")
			report.Warnings = append(report.Warnings,
				"настройки сохранены, но применить их к домофонии и потокам не удалось: "+err.Error())
		}
	}

	writeJSON(w, http.StatusOK, report)
}
