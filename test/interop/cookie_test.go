package interop

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
)

// Защита WireGuard от handshake-флуда: нода «под нагрузкой» (очередь handshake'ов
// >= 128) на initiation без верного MAC2 отвечает cookie-reply, и инициатор обязан
// повторить initiation с MAC2, посчитанным из cookie. Это отдельный тип пакета (S3/H3),
// который обычный трафик не задействует. В смешанном mesh (часть нод на 0.2.x) обмен
// cookie обязан работать между версиями — иначе флуд на публичный порт отрезает от
// неё ноды другой версии.
//
// Тест доказывает, а не надеется: (1) публичная нода реально под нагрузкой
// (IsUnderLoad самой библиотеки); (2) cookie-reply реально дошёл до сокета инициатора;
// (3) пока флуд продолжается, handshake завершается и трафик идёт — значит, cookie
// понят и MAC2 принят.
func TestInterop_CookieReplyUnderLoad(t *testing.T) {
	combos := [][2]string{{"old", "new"}, {"new", "old"}, {"new", "new"}, {"old", "old"}}
	for _, c := range combos {
		t.Run(c[0]+"-nat/"+c[1]+"-pub", func(t *testing.T) {
			p := generate(t)
			r := connectUnderLoad(t, c[0], c[1], p, p, 20*time.Second)
			if !r.connected {
				t.Fatalf("под флудом не связались (cookie-reply получено: %d)", r.cookies)
			}
			if r.cookies == 0 {
				t.Fatal("cookie-reply до инициатора не дошёл — путь cookie не задействован, тест ничего не доказал")
			}
			if !r.underLoadAtEnd {
				t.Fatal("к моменту handshake'а pub вышла из-под нагрузки — MAC2 мог не проверяться")
			}
			if !reachable(t, r.pub, r.nat, upWithin) {
				t.Fatal("pub→nat не доходит после handshake'а через cookie")
			}
		})
	}
}

// Сторож самого теста: S3 влияет ТОЛЬКО на cookie-reply. С расхождением S3 пара обязана
// связываться без флуда (контроль) и НЕ связываться под флудом (инициатор не разбирает
// cookie → не может посчитать MAC2). Если под флудом связь есть — тест выше не проверяет
// cookie (device не был под нагрузкой или MAC2 не проверялся), и его зелёный ничего не значит.
func TestInterop_CookieHarnessDependsOnCookie(t *testing.T) {
	pubP := generate(t)
	natP := pubP
	natP.S3++

	nat, pub := pair(t, "new", "new", natP, awgparams.LocalObf{}, awgparams.LocalObf{})
	if !reachable(t, nat, pub, upWithin) {
		t.Fatal("контроль: без флуда расхождение S3 не должно мешать связи")
	}

	r := connectUnderLoad(t, "new", "new", natP, pubP, 12*time.Second)
	if r.cookies == 0 {
		t.Fatal("под флудом pub не прислала cookie-reply — нагрузка не достигнута")
	}
	if r.connected {
		t.Fatal("под флудом связались, хотя инициатор не может разобрать cookie — MAC2 не проверялся, cookie-тест ничего не доказывает")
	}
}

type loadResult struct {
	nat, pub       *node
	connected      bool
	cookies        int64
	underLoadAtEnd bool
}

// connectUnderLoad: pub (pubParams) держится под флудом, nat (natParams) стучится к ней.
// cookies — сколько cookie-reply в формате pub дошло до сокета nat.
func connectUnderLoad(t *testing.T, natLib, pubLib string, natParams, pubParams awgparams.Params, within time.Duration) loadResult {
	t.Helper()
	nat := newNode(t, natLib, "nat", "100.64.0.10")
	pub := newNode(t, pubLib, "pub", "100.64.0.11")
	pub.configure(t, pubParams, awgparams.LocalObf{}, peerUAPI(t, nat, ""))
	port := pub.port(t)

	// Один поток: приёмник device вычитывает пачку датаграмм (до 128 за recvmmsg) и
	// кладёт её в очередь handshake'ов раньше, чем обработчики её разберут, — нагрузка
	// детерминирована. На многоядерной машине MAC1-отсев успевает за флудом, и очередь до
	// порога «под нагрузкой» доходит через раз.
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)

	stop := startHandshakeFlood(t, port, pubParams)
	defer stop()
	stopKeeper := keepUnderLoad(pub)
	defer stopKeeper()
	waitUnderLoad(t, pub)

	shape := pubParams // ищем cookie-reply в формате ОТПРАВИТЕЛЯ — что реально пришло по проводу
	nat.cookieShape.Store(&shape)
	nat.configure(t, natParams, awgparams.DefaultLocalObf(), peerUAPI(t, pub, fmt.Sprintf("127.0.0.1:%d", port)))

	// Ретрай initiation с MAC2 идёт по таймеру REKEY_TIMEOUT (5 с + джиттер).
	connected := reachable(t, nat, pub, within)
	return loadResult{nat: nat, pub: pub, connected: connected, cookies: nat.cookies.Load(), underLoadAtEnd: pub.d.IsUnderLoad()}
}

// startHandshakeFlood заливает порт мусорными initiation (размер S1+148, тип из H1):
// device ставит их в очередь handshake'ов, где они отсеиваются по MAC1. Флуд — ИМПУЛЬСАМИ:
// пачка поднимает очередь выше порога «под нагрузкой» (128), а статус держится секунду
// после пика, — так device всё время «под нагрузкой», но очередь не забита до предела
// (1024): при забитой настоящая initiation просто не влезает и отбрасывается, и тест
// проверял бы переполнение, а не cookie. Возвращает stop.
func startHandshakeFlood(t *testing.T, port int, p awgparams.Params) (stop func()) {
	t.Helper()
	const (
		burst = 300                    // > 128 (порог), << 1024 (ёмкость очереди)
		pause = 150 * time.Millisecond // << 1 с (сколько держится «под нагрузкой»)
	)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer conn.Close()
		pkt := make([]byte, p.S1+148)
		for {
			for i := 0; i < burst; i++ {
				_, _ = rand.Read(pkt)
				binary.LittleEndian.PutUint32(pkt[p.S1:], p.H1.Min)
				_, _ = conn.Write(pkt)
			}
			select {
			case <-done:
				return
			case <-time.After(pause):
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

// keepUnderLoad держит статус «под нагрузкой» на всё время флуда. Device обновляет его
// (IsUnderLoad: очередь >= 128 → «под нагрузкой ещё 1 с») только на пакетах с ВЕРНЫМ
// MAC1, а мусор флуда отсеивается раньше — к моменту повторной initiation (через 5 с)
// статус успевал бы истечь, и device принял бы её БЕЗ проверки MAC2: тест прошёл бы, не
// доказав совместимость cookie. Частый опрос во время пачек делает то же, что сделал бы
// флуд с верным MAC1 (атакующий, знающий публичный ключ).
func keepUnderLoad(n *node) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
				n.d.IsUnderLoad()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

func waitUnderLoad(t *testing.T, n *node) {
	t.Helper()
	deadline := time.Now().Add(upWithin)
	for !n.d.IsUnderLoad() {
		if time.Now().After(deadline) {
			t.Fatalf("%s(%s): флуд не загнал device под нагрузку — cookie не проверить", n.name, n.lib)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
