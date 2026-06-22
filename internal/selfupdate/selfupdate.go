// Package selfupdate — низкоуровневые примитивы self-upgrade: атомарная замена
// бинаря, detached-spawn watchdog'а, version-probe и разворот пути к своему
// бинарю. Оркестрация апгрейда (режимы apk/file, watchdog-цикл) — в cmd/meshd;
// здесь только переиспользуемые, платформо-осознанные операции с файлами/процессами.
//
// Платформо-зависимая отвязка процесса (setsid / DETACHED_PROCESS) — за build-tags
// в spawn_unix.go / spawn_windows.go.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// versionProbeTimeout — лимит на `<bin> version` (заодно проверка исполнимости).
const versionProbeTimeout = 10 * time.Second

// ResolveSelfPath — абсолютный путь текущего бинаря с разворотом симлинков.
func ResolveSelfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// BinaryVersion запускает `<path> version`. Заодно это проверка, что бинарь
// вообще исполняем под текущую арку (кривой/чужой отвалится с exec-ошибкой).
func BinaryVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("empty version output")
	}
	return v, nil
}

// SpawnDetached запускает процесс, отвязанный от нашей сессии: разрыв awg0/SSH
// не пошлёт ему SIGHUP. stdout+stderr → logPath, stdin → /dev/null. Не ждём
// завершения — процесс переживает родителя. Способ отвязки платформенный
// (detachSysProcAttr: setsid на unix, DETACHED_PROCESS на windows).
func SpawnDetached(name string, args []string, logPath string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = detachSysProcAttr()

	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logf.Close()
	cmd.Stdout = logf
	cmd.Stderr = logf

	if devnull, err := os.Open(os.DevNull); err == nil {
		cmd.Stdin = devnull
		defer devnull.Close()
	}

	return cmd.Start()
}

// CopyFile копирует src→dst с правами mode (перезаписывая dst).
func CopyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src) // бинарь ~8 MB — спокойно влезает в RAM
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode) // WriteFile не трогает права, если файл уже был
}

// ReplaceBinary атомарно заменяет target содержимым src: пишем target.new и
// rename'им поверх. Работающий процесс держит старый inode, поэтому ETXTBSY
// (text file busy) не словим, а rename атомарен в пределах одной FS.
func ReplaceBinary(target, src string, mode os.FileMode) error {
	tmp := target + ".new"
	if err := CopyFile(src, tmp, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
