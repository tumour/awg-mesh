//go:build windows

package state

import (
	"os"
	"path/filepath"
)

// DefaultPath — стандартное место state.json на Windows (%ProgramData%\meshd).
// ProgramData — машинно-глобальный путь, доступный службе.
var DefaultPath = filepath.Join(os.Getenv("ProgramData"), "meshd", "state.json")
