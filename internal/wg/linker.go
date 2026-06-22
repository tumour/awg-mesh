package wg

// Linker — ОС-зависимая настройка L3 на сетевом линке (kernel-side): назначение
// адреса и up/down/delete интерфейса. Отделён от userspace-device (тот кросс-
// платформенный через amneziawg-go) — здесь собран ВЕСЬ ip-link lifecycle в одном
// месте, за build-tags. Узкий порт: вторую реализацию даёт Windows (IP Helper API).
//
// Семантика идемпотентна: AddIP не падает на уже назначенном адресе, Delete — на
// отсутствующем интерфейсе (ОС-специфика «уже есть»/«нет такого» спрятана в реализации).
type Linker interface {
	AddIP(iface, cidr string) error // назначить адрес на интерфейс (idempotent)
	SetUp(iface string) error       // поднять линк
	SetDown(iface string) error     // опустить линк
	Delete(iface string) error      // удалить интерфейс (idempotent)
}

// DefaultLinker — ОС-специфичная реализация Linker (выбор за build-tags:
// linker_unix.go / linker_windows.go).
func DefaultLinker() Linker { return newLinker() }
