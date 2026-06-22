//go:build windows

package selfupdate

import "syscall"

// detachSysProcAttr — windows-вариант отвязки процесса от родителя: аналог
// setsid. DETACHED_PROCESS (0x8) убирает консоль родителя, CREATE_NEW_PROCESS_GROUP
// (0x200) выносит в свою группу — потомок переживает завершение/разрыв родителя.
//
// NB: остальная Windows-обвязка (замена залоченного .exe, служба, IP Helper API
// вместо `ip`) — в backlog; self-upgrade на Windows ещё требует доработки.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}
