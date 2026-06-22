// Package bootstrap — серверная сторона bootstrap-протокола: Noise IKpsk2
// handshake поверх TCP, валидация cluster-secret (PSK), регистрация новой ноды
// (mesh.RegisterPeer) и ответ peer-list'ом. Один listener для обоих режимов:
// `meshd serve` (debug, onRegistered=nil) и демон `meshd run` (onRegistered
// пушит нового peer'а в live wg-device).
package bootstrap

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/flynn/noise"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

const (
	// handshakeMaxFrame — лимит на незашифрованные кадры handshake (msg1/msg2).
	handshakeMaxFrame = 2048
	// connDeadline — дедлайн на весь bootstrap-обмен с одним соединением.
	connDeadline = 30 * time.Second
	// dialTimeout — таймаут на коннект клиента к seed.
	dialTimeout = 10 * time.Second
	// maxConcurrentHandshakes — потолок одновременных Noise-handshake'ей. Порт
	// торчит наружу, а IKpsk2 считает msg1/msg2 (Curve25519 DH) ДО любой проверки
	// PSK — без лимита поток коннектов истощил бы CPU/горутины/FD. Лишние коннекты
	// ждут слот (backpressure через kernel-backlog), а не плодят горутины.
	maxConcurrentHandshakes = 64
)

// Serve запускает bootstrap accept-loop на addr. onRegistered (если не nil)
// вызывается после каждого join'а — демон через него пушит свежий peer-list в
// live wg-device. logger (nil → slog.Default()) инъектируется для embeddability.
// Останавливается при отмене ctx. Блокирующая.
func Serve(
	ctx context.Context,
	addr string,
	store *state.Store,
	priv wgkey.Private,
	pub wgkey.Public,
	psk []byte,
	onRegistered func(),
	logger *slog.Logger,
) error {
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "bootstrap")

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()
	go func() { <-ctx.Done(); _ = listener.Close() }()

	// Семафор ограничивает число handshake'ей в полёте (см. maxConcurrentHandshakes).
	sem := make(chan struct{}, maxConcurrentHandshakes)

	logger.Info("listening", "addr", addr, "proto", "tcp")
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Warn("accept failed", "err", err)
			continue
		}
		// Берём слот ДО спавна горутины — при переполнении accept-loop притормозит
		// (backpressure), а не наплодит горутин. Отмена ctx разблокирует ожидание.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			_ = conn.Close()
			return nil
		}
		go func(c net.Conn) {
			defer func() { <-sem }()
			// recover: паника на кривом фрейме/вводе с публичного порта не должна
			// класть весь демон — гасим её в пределах одного соединения.
			defer func() {
				if r := recover(); r != nil {
					logger.Error("panic handling connection", "remote", c.RemoteAddr().String(), "panic", r)
				}
			}()
			// onRegistered (push в device) — только после УСПЕШНОЙ регистрации.
			if handleConn(c, store, priv, pub, psk, logger) && onRegistered != nil {
				onRegistered()
			}
		}(conn)
	}
}

// Join — клиентская сторона bootstrap'а: коннект к seed, Noise IKpsk2 handshake,
// HelloRequest → HelloResponse. Возвращает ответ seed'а (выделенный IP, awg-params,
// peer-list). Чистый transport — caller (cmd join) готовит ключи/req и сохраняет
// state из ответа.
func Join(
	seedEndpoint string,
	psk []byte,
	priv wgkey.Private,
	pub wgkey.Public,
	seedPub []byte,
	req proto.HelloRequest,
) (*proto.HelloResponse, error) {
	hs, err := handshake.InitiatorHandshake(priv[:], pub[:], seedPub, psk)
	if err != nil {
		return nil, fmt.Errorf("init handshake: %w", err)
	}

	conn, err := net.DialTimeout("tcp", seedEndpoint, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial seed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(connDeadline))

	// Message 1: client → server
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("noise msg1: %w", err)
	}
	if err := proto.WriteFrame(conn, msg1); err != nil {
		return nil, fmt.Errorf("send msg1: %w", err)
	}

	// Message 2: server → client (+ CipherStates)
	msg2, err := proto.ReadFrame(conn, handshakeMaxFrame)
	if err != nil {
		return nil, fmt.Errorf("read msg2: %w", err)
	}
	_, csInitToResp, csRespToInit, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, fmt.Errorf("noise msg2 (wrong secret or seed pubkey?): %w", err)
	}

	if err := proto.WriteMessage(conn, csInitToResp, req); err != nil {
		return nil, fmt.Errorf("send hello-req: %w", err)
	}
	var resp proto.HelloResponse
	if err := proto.ReadMessage(conn, csRespToInit, &resp); err != nil {
		return nil, fmt.Errorf("read hello-resp: %w", err)
	}
	return &resp, nil
}

// handleConn — обработка одного bootstrap-join'а: Noise IK handshake →
// HelloRequest → mesh.RegisterPeer → HelloResponse. Возвращает true, только если
// регистрация прошла успешно (caller тогда дёргает onRegistered).
func handleConn(
	conn net.Conn,
	store *state.Store,
	priv wgkey.Private,
	pub wgkey.Public,
	psk []byte,
	logger *slog.Logger,
) bool {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(connDeadline))

	// Per-conn логгер: remote-адрес как атрибут на всех строках этого соединения.
	clog := logger.With("remote", conn.RemoteAddr().String())
	clog.Info("new bootstrap connection")

	hs, err := handshake.ResponderHandshake(priv[:], pub[:], psk)
	if err != nil {
		clog.Warn("responder handshake init failed", "err", err)
		return false
	}

	// Message 1 (client → server)
	msg1, err := proto.ReadFrame(conn, handshakeMaxFrame)
	if err != nil {
		clog.Warn("read msg1 failed", "err", err)
		return false
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		clog.Warn("noise msg1 failed (wrong secret or seed pubkey?)", "err", err)
		return false
	}

	// Message 2 (server → client) + установка CipherState'ов. flynn/noise
	// возвращает (c_init_to_resp, c_resp_to_init) одинаково в Write/ReadMessage:
	// сервер шифрует через c_resp_to_init, расшифровывает через c_init_to_resp.
	out, csClientToServer, csServerToClient, err := hs.WriteMessage(nil, nil)
	if err != nil {
		clog.Warn("write msg2 failed", "err", err)
		return false
	}
	if err := proto.WriteFrame(conn, out); err != nil {
		clog.Warn("send msg2 failed", "err", err)
		return false
	}

	// Шифрованный канал готов. Читаем HelloRequest.
	var req proto.HelloRequest
	if err := proto.ReadMessage(conn, csClientToServer, &req); err != nil {
		clog.Warn("read hello-req failed", "err", err)
		return false
	}
	if req.Version != proto.ProtoVersion {
		clog.Warn("proto version mismatch", "client", req.Version, "server", proto.ProtoVersion)
		respondErr(conn, csServerToClient, fmt.Sprintf(
			"proto version mismatch (server=%d, client=%d)",
			proto.ProtoVersion, req.Version))
		return false
	}

	// Identity binding: pubkey, заявленный в HelloRequest, обязан совпадать со
	// static-ключом, который Noise УЖЕ аутентифицировал в msg1 (hs.PeerStatic()).
	// Иначе клиент, знающий cluster-secret, прошёл бы хендшейк ключом A, а
	// зарегистрировал бы произвольный чужой pubkey B — фантомная запись в mesh.
	authedPub := base64.StdEncoding.EncodeToString(hs.PeerStatic())
	if req.PublicKey != authedPub {
		clog.Warn("identity mismatch: claimed pubkey != Noise static key",
			"claimed", shortKey(req.PublicKey), "authenticated", shortKey(authedPub))
		respondErr(conn, csServerToClient, "identity mismatch")
		return false
	}

	// Endpoint опционален (NAT-ноды его не объявляют), но если есть — обязан быть
	// валидным host:port: он уйдёт всем нодам в UAPI wg-device.
	if req.Endpoint != "" {
		if _, _, err := net.SplitHostPort(req.Endpoint); err != nil {
			clog.Warn("invalid endpoint", "endpoint", req.Endpoint, "label", req.Label, "err", err)
			respondErr(conn, csServerToClient,
				fmt.Sprintf("invalid endpoint %q (want host:port)", req.Endpoint))
			return false
		}
	}

	// Регистрация атомарна: идемпотентность, аллокация IP и append (доменная
	// логика mesh.RegisterPeer) происходят под одним локом Store — параллельный
	// join или gossip-merge не потеряют запись и не выдадут дубликат IP.
	var reg mesh.Registration
	snap, err := store.Update(func(s *state.State) error {
		r, err := mesh.RegisterPeer(s, mesh.JoinRequest{
			Label:     req.Label,
			PublicKey: req.PublicKey,
			Endpoint:  req.Endpoint,
		})
		if err != nil {
			return err
		}
		reg = r
		if !r.Changed {
			return state.ErrNoChange // re-join без изменений — не пишем файл
		}
		return nil
	})
	if err != nil {
		// Полную причину — только в наш лог; клиенту generic (не сливаем
		// внутренние строки ошибок в открытый канал).
		clog.Warn("register peer failed", "label", req.Label, "err", err)
		respondErr(conn, csServerToClient, "registration failed")
		return false
	}

	if reg.Rejoined {
		clog.Info("peer re-joined (returning existing)", "label", req.Label, "ip", reg.AssignedIP)
	} else {
		clog.Info("peer registered", "label", req.Label, "ip", reg.AssignedIP,
			"endpoint", req.Endpoint, "pubkey", shortKey(req.PublicKey))
	}

	respondOK(conn, csServerToClient, snap, reg.AssignedIP, clog)
	return true
}

func respondErr(conn net.Conn, cs *noise.CipherState, msg string) {
	_ = proto.WriteMessage(conn, cs, proto.HelloResponse{
		Version: proto.ProtoVersion,
		Status:  "error",
		Error:   msg,
	})
}

func respondOK(conn net.Conn, cs *noise.CipherState, s *state.State, yourIP string, logger *slog.Logger) {
	resp := proto.HelloResponse{
		Version:     proto.ProtoVersion,
		Status:      "ok",
		YourIP:      yourIP,
		NetworkCIDR: s.NetworkCIDR,
		AwgParams:   s.AwgParams,
		WGPort:      s.ListenPort,
		Peers:       proto.PeerInfosFromState(s.Peers),
	}
	if err := proto.WriteMessage(conn, cs, resp); err != nil {
		logger.Warn("write hello-resp failed", "err", err)
	}
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:8] + "..."
	}
	return k
}
