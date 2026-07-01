package api

import (
	"net/http"
	"time"
)

// get — обёртка, пускающая только GET/HEAD; иначе канонический 405 с Allow.
// Метод проверяем внутри хендлера (а не method-pattern'ом mux'а), чтобы тело
// ответа было нашим JSON-конвертом, а не дефолтным text/plain от ServeMux.
func (s *Server) get(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, s.log, http.MethodGet)
			return
		}
		h(w, r)
	}
}

// recoverPanic — паника в хендлере не роняет процесс, а превращается в 500.
// Имя не `recover`, чтобы не путать с builtin recover().
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "err", rec, "path", r.URL.Path)
				internalError(w, s.log)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logRequests — структурный лог каждого запроса на Debug (метод, путь, статус,
// длительность). Debug, чтобы не шуметь в обычном режиме.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		s.log.Debug("request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "dur", time.Since(start))
	})
}

// statusRecorder перехватывает записанный статус для логирования (по умолчанию
// 200, если хендлер не звал WriteHeader явно).
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}
