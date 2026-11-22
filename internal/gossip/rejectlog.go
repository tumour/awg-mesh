package gossip

import "sync"

// versionOnce — «залогировать отказ в Pending один раз на версию». Отказ повторяется на
// каждом цикле доставки (push от seed каждые ~10 с, pull — каждый gossip-интервал), а
// лог на каждый повтор вымывает маленький RAM-буфер логов роутера за день-два. Версии
// Pending монотонны, поэтому достаточно помнить максимальную залогированную.
type versionOnce struct {
	mu  sync.Mutex
	max uint64
}

// first сообщает, нужно ли логировать отказ версии v: true ровно один раз на каждую
// версию, превышающую все ранее залогированные. Безопасен для конкурентного вызова.
func (o *versionOnce) first(v uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v <= o.max {
		return false
	}
	o.max = v
	return true
}
