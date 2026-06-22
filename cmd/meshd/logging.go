package main

import (
	"log/slog"
	"os"
)

// newDaemonLogger — структурный логгер демона: текстовый хендлер в stderr
// (systemd/journald и procd/syslog его подхватывают). Уровень Info.
//
// CLI-команды (init/join/status/token) печатают результат через fmt в stdout —
// это пользовательский вывод, не логи, и логгер им не нужен.
func newDaemonLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
