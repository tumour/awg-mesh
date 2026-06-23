package state

import (
	"errors"
	"os"
	"sync"
)

// ErrNoChange — sentinel для Update: fn сигналит "state не менялся, на диск
// не пиши". Update вернёт текущий state и nil-ошибку.
var ErrNoChange = errors.New("state unchanged")

// Store — потокобезопасный владелец state-файла.
//
// Все компоненты одного процесса (bootstrap-listener, gossip-клиент,
// gossip-сервер) обязаны ходить к state через ОБЩИЙ Store: голые Load/Save
// из разных goroutine — это read-modify-write гонка с потерей записей
// (worst case: регистрация peer'а затирается gossip-merge'ем, и следующий
// join получает тот же mesh-IP).
//
// Внутри процесса сериализует mutex; МЕЖДУ процессами (meshd join при живом
// meshd run — разные процессы, разные mutex'ы, один файл) Update берёт ещё и
// flock на <path>.lock. Без него resume-join и gossip-merge затёрли бы друг
// друга last-write-wins → потерянная регистрация → дубль mesh-IP. На Windows
// flock пока no-op (демон под Windows не цель, см. lock_windows.go).
type Store struct {
	mu   sync.Mutex
	path string
}

// NewStore создаёт Store над файлом path ("" = DefaultPath).
func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath
	}
	return &Store{path: path}
}

// lockState берёт cross-process flock на стабильном <path>.lock (НЕ на самом
// state: его rename при Save меняет inode и сломал бы flock). Возвращает
// unlock-функцию, либо nil, если открыть файл или взять лок не удалось.
func lockState(path string) func() {
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil
	}
	if err := flockExclusive(lf); err != nil {
		_ = lf.Close()
		return nil
	}
	return func() { flockUnlock(lf); _ = lf.Close() }
}

// Read возвращает свежий снапшот state с диска.
func (st *Store) Read() (*State, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return Load(st.path)
}

// Update атомарно выполняет read-modify-write: Load → fn → Save.
// fn получает свежий state и мутирует его in-place. Возврат ErrNoChange
// из fn пропускает запись на диск (не ошибка). Возвращает state после fn —
// удобно отвечать peer-list'ом без повторного Read.
func (st *Store) Update(fn func(*State) error) (*State, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Cross-process lock; nil-unlock = лок взять не вышло, продолжаем только под
	// внутрипроцессным mutex'ом (best-effort).
	if unlock := lockState(st.path); unlock != nil {
		defer unlock()
	}

	s, err := Load(st.path)
	if err != nil {
		return nil, err
	}
	if err := fn(s); err != nil {
		if errors.Is(err, ErrNoChange) {
			return s, nil
		}
		return nil, err
	}
	if err := s.Save(st.path); err != nil {
		return nil, err
	}
	return s, nil
}
