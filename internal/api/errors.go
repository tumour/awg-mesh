package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// apiError — тело ошибки под конвертом "error". Клиенту уходит ТОЛЬКО code +
// generic message: внутреннюю причину логируем у себя (инвариант репо — не сливать
// внутренние строки в открытый канал). Details зарезервировано под field-level
// ошибки валидации (появятся с мутациями), omitempty — на read-путях пусто.
type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

// writeError отдаёт структурированную ошибку. msg — уже безопасный для клиента
// текст; внутреннюю причину логируй ОТДЕЛЬНО до вызова.
func writeError(w http.ResponseWriter, log *slog.Logger, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorEnvelope{Error: apiError{Code: code, Message: msg}}); err != nil {
		log.Warn("encode error response failed", "err", err)
	}
}

// Стандартные ответы-ошибки — единые generic-сообщения, без утечки деталей.

func notFound(w http.ResponseWriter, log *slog.Logger) {
	writeError(w, log, http.StatusNotFound, "not_found", "resource not found")
}

func methodNotAllowed(w http.ResponseWriter, log *slog.Logger, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, log, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func internalError(w http.ResponseWriter, log *slog.Logger) {
	writeError(w, log, http.StatusInternalServerError, "internal", "internal error")
}
