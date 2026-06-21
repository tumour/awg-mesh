//go:build !windows

package main

import "syscall"

// detachSysProcAttr — unix-вариант отвязки процесса от сессии: Setsid создаёт
// новую сессию, и разрыв управляющего терминала (SSH) не пошлёт процессу SIGHUP.
// Это и держит watchdog/рестарт живыми при пересоздании awg0.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
