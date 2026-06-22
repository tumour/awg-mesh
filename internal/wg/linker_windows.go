//go:build windows

package wg

import "fmt"

// winLinker — заглушка под Windows. amneziawg-go поднимает wintun-адаптер, но
// L3-настройка (адрес, up/down/delete) идёт не через `ip`, а через IP Helper API
// (CreateUnicastIpAddressEntry и пр.) — это часть Windows-обвязки из backlog'а.
// Сейчас линкуется и компилируется, но возвращает ошибку при реальном вызове.
type winLinker struct{}

func newLinker() Linker { return winLinker{} }

func (winLinker) AddIP(iface, cidr string) error { return errTODO("AddIP") }
func (winLinker) SetUp(iface string) error       { return errTODO("SetUp") }
func (winLinker) SetDown(iface string) error     { return errTODO("SetDown") }
func (winLinker) Delete(iface string) error      { return errTODO("Delete") }

func errTODO(op string) error {
	return fmt.Errorf("wg.Linker.%s: link config on windows not implemented (IP Helper API TODO)", op)
}
