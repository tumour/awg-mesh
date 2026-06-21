//go:build !windows

package state

// DefaultPath — стандартное место state.json на unix (директория chmod 700,
// файл chmod 600, owned by root).
var DefaultPath = "/etc/meshd/state.json"
