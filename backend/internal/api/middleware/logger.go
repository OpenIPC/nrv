package middleware

import (
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(ww, r)

		log.Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("remote", r.RemoteAddr).
			Int("status", ww.statusCode).
			Dur("duration", time.Since(start)).
			Msg("request")
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush пробрасывает принудительную отправку данных клиенту.
//
// Без этого метода обёртка логгера скрывает, что настоящий ответ умеет
// выталкивать данные, и проверка `w.(http.Flusher)` в обработчиках даёт
// отказ: поток событий (SSE) отвечал «сервер не умеет отдавать поток»,
// хотя под обёрткой лежал обычный HTTP-ответ, который всё умеет.
//
// Важно не только для нашего потока: так же ломались бы любые другие
// обработчики, использующие возможности ответа.
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap открывает доступ к исходному ответу.
//
// Им пользуется http.NewResponseController (Go 1.20+): без Unwrap он не
// может добраться до того, что умеет настоящий ответ, и возвращает
// «не поддерживается».
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
