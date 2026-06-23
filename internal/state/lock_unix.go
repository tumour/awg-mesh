//go:build !windows

package state

import (
	"os"
	"syscall"
)

// flockExclusive берёт эксклюзивный advisory-лок на f (блокирующе). Снимает
// cross-process гонку join↔daemon на одном state-файле: внутрипроцессного
// mutex'а Store для этого мало — meshd join и meshd run это РАЗНЫЕ процессы.
func flockExclusive(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func flockUnlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
