//go:build windows

package state

import (
	"os"
	"path/filepath"
)

// DefaultPath — стандартное место state.json на Windows (%ProgramData%\meshd).
// ProgramData — машинно-глобальный путь, доступный службе; при пустом env
// (нестандартное окружение) падаем на стандартный C:\ProgramData, чтобы не
// получить битый относительный путь.
var DefaultPath = filepath.Join(programData(), "meshd", "state.json")

func programData() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return pd
	}
	return `C:\ProgramData`
}
