// Package fsutil — durable атомарная запись файла (tmp → fsync файла → rename →
// fsync директории).
//
// Зачем fsync: голый tmp+rename даёт атомарность против КОНКУРЕНТНОГО ЧТЕНИЯ, но
// НЕ durability против потери питания. На ext4 (data=ordered), а тем более на
// jffs2/overlay роутера, rename без предварительного fsync классически
// материализуется как файл НУЛЕВОЙ длины после внезапного ребута. Целевая
// платформа (роутеры/VPS) выключается из розетки, а здесь пишутся вещи, потеря
// которых = кирпич: state.json (ключи, cluster identity) и /usr/bin/meshd
// (self-upgrade). Поэтому durable-write вынесен в один примитив.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile durable и атомарно пишет data в path с правами perm. tmp создаётся в
// той же директории (rename атомарен только в пределах одной FS) и подчищается
// при ошибке. Гарантирует, что и данные файла, и dir-entry от rename долетели до
// диска (fsync файла + fsync директории).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// fsync файла ДО rename — иначе rename может долететь раньше данных.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	// O_CREATE учитывает umask — добиваем точные права явно.
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	// fsync директории — чтобы запись dir-entry от rename тоже стала durable.
	// Best-effort: на части ОС (Windows) fsync каталога не поддержан.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
