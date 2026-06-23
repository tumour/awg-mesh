//go:build windows

package state

import "os"

// Windows: cross-process flock-аналог (LockFileEx) пока не реализован — демон под
// Windows ещё не цель. No-op: внутрипроцессного mutex'а Store достаточно для
// одного процесса; межпроцессную безопасность здесь добавим вместе с остальной
// Windows-обвязкой (служба, IP Helper API).
func flockExclusive(*os.File) error { return nil }

func flockUnlock(*os.File) {}
