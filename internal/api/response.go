package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// envelope — единый success-конверт API: полезная нагрузка всегда под "data"
// (как Laravel Resource). meta зарезервировано под пагинацию/курсоры (позже),
// omitempty — пока не заполняем, конверт остаётся `{"data":...}`.
type envelope struct {
	Data any            `json:"data"`
	Meta map[string]any `json:"meta,omitempty"`
}

// writeJSON сериализует data под success-конвертом со статусом status.
// Заголовки пишем ДО WriteHeader. Ошибку энкодинга починить уже нельзя (статус
// ушёл) — фиксируем в лог, тело клиента останется усечённым.
func writeJSON(w http.ResponseWriter, log *slog.Logger, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(envelope{Data: data}); err != nil {
		log.Warn("encode response failed", "err", err)
	}
}
