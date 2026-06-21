// Package bootstrap — серверная сторона bootstrap-протокола: Noise IKpsk2
// handshake поверх TCP, валидация cluster-secret (PSK), регистрация новой ноды
// (mesh.RegisterPeer) и ответ peer-list'ом. Один listener для обоих режимов:
// `meshd serve` (debug, onRegistered=nil) и демон `meshd run` (onRegistered
// пушит нового peer'а в live wg-device).
package bootstrap

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/flynn/noise"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// handshakeMaxFrame — лимит на незашифрованные кадры handshake (msg1/msg2).
const handshakeMaxFrame = 2048

// Serve запускает bootstrap accept-loop на addr. onRegistered (если не nil)
// вызывается после каждого join'а — демон через него пушит свежий peer-list в
// live wg-device. Останавливается при отмене ctx. Блокирующая.
func Serve(
	ctx context.Context,
	addr string,
	store *state.Store,
	priv wgkey.Private,
	pub wgkey.Public,
	psk []byte,
	onRegistered func(),
) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()
	go func() { <-ctx.Done(); _ = listener.Close() }()

	log.Printf("bootstrap: listening on %s/tcp", addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("bootstrap: accept: %v", err)
			continue
		}
		go func(c net.Conn) {
			handleConn(c, store, priv, pub, psk)
			if onRegistered != nil {
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

	conn, err := net.DialTimeout("tcp", seedEndpoint, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial seed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Message 1: client → server
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("noise msg1: %w", err)
	}
	if err := writeFramed(conn, msg1); err != nil {
		return nil, fmt.Errorf("send msg1: %w", err)
	}

	// Message 2: server → client (+ CipherStates)
	msg2, err := readFramed(conn, handshakeMaxFrame)
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
// HelloRequest → mesh.RegisterPeer → HelloResponse.
func handleConn(
	conn net.Conn,
	store *state.Store,
	priv wgkey.Private,
	pub wgkey.Public,
	psk []byte,
) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	remoteAddr := conn.RemoteAddr().String()
	log.Printf("[%s] new bootstrap-connection", remoteAddr)

	hs, err := handshake.ResponderHandshake(priv[:], pub[:], psk)
	if err != nil {
		log.Printf("[%s] responder-handshake init: %v", remoteAddr, err)
		return
	}

	// Message 1 (client → server)
	msg1, err := readFramed(conn, handshakeMaxFrame)
	if err != nil {
		log.Printf("[%s] read msg1: %v", remoteAddr, err)
		return
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		log.Printf("[%s] noise msg1 fail (wrong secret or wrong seed pubkey?): %v", remoteAddr, err)
		return
	}

	// Message 2 (server → client) + установка CipherState'ов. flynn/noise
	// возвращает (c_init_to_resp, c_resp_to_init) одинаково в Write/ReadMessage:
	// сервер шифрует через c_resp_to_init, расшифровывает через c_init_to_resp.
	out, csClientToServer, csServerToClient, err := hs.WriteMessage(nil, nil)
	if err != nil {
		log.Printf("[%s] write msg2: %v", remoteAddr, err)
		return
	}
	if err := writeFramed(conn, out); err != nil {
		log.Printf("[%s] send msg2: %v", remoteAddr, err)
		return
	}

	// Шифрованный канал готов. Читаем HelloRequest.
	var req proto.HelloRequest
	if err := proto.ReadMessage(conn, csClientToServer, &req); err != nil {
		log.Printf("[%s] read hello-req: %v", remoteAddr, err)
		return
	}
	if req.Version != proto.ProtoVersion {
		log.Printf("[%s] proto-version mismatch: client=%d server=%d",
			remoteAddr, req.Version, proto.ProtoVersion)
		respondErr(conn, csServerToClient, fmt.Sprintf(
			"proto version mismatch (server=%d, client=%d)",
			proto.ProtoVersion, req.Version))
		return
	}

	// Endpoint опционален (NAT-ноды его не объявляют), но если есть — обязан быть
	// валидным host:port: он уйдёт всем нодам в UAPI wg-device.
	if req.Endpoint != "" {
		if _, _, err := net.SplitHostPort(req.Endpoint); err != nil {
			log.Printf("[%s] invalid endpoint %q from %s: %v",
				remoteAddr, req.Endpoint, req.Label, err)
			respondErr(conn, csServerToClient,
				fmt.Sprintf("invalid endpoint %q (want host:port)", req.Endpoint))
			return
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
		log.Printf("[%s] register peer %s: %v", remoteAddr, req.Label, err)
		respondErr(conn, csServerToClient, err.Error())
		return
	}

	if reg.Rejoined {
		log.Printf("[%s] peer %s already registered (ip=%s), returning existing",
			remoteAddr, req.Label, reg.AssignedIP)
	} else {
		log.Printf("[%s] registered peer %s as %s (endpoint=%q, pubkey=%s)",
			remoteAddr, req.Label, reg.AssignedIP, req.Endpoint, shortKey(req.PublicKey))
	}

	respondOK(conn, csServerToClient, snap, reg.AssignedIP)
}

func respondErr(conn net.Conn, cs *noise.CipherState, msg string) {
	_ = proto.WriteMessage(conn, cs, proto.HelloResponse{
		Version: proto.ProtoVersion,
		Status:  "error",
		Error:   msg,
	})
}

func respondOK(conn net.Conn, cs *noise.CipherState, s *state.State, yourIP string) {
	peers := make([]proto.PeerInfo, 0, len(s.Peers))
	for _, p := range s.Peers {
		peers = append(peers, proto.PeerInfo{
			Label:     p.Label,
			PublicKey: p.PublicKey,
			Endpoint:  p.Endpoint,
			NodeIP:    p.NodeIP,
			IsSeed:    p.IsSeed,
		})
	}
	resp := proto.HelloResponse{
		Version:     proto.ProtoVersion,
		Status:      "ok",
		YourIP:      yourIP,
		NetworkCIDR: s.NetworkCIDR,
		AwgParams:   s.AwgParams,
		WGPort:      s.ListenPort,
		Peers:       peers,
	}
	if err := proto.WriteMessage(conn, cs, resp); err != nil {
		log.Printf("write hello-resp: %v", err)
	}
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:8] + "..."
	}
	return k
}

// readFramed читает 2-байт length prefix + body. io.ReadFull (не conn.Read):
// последний может вернуть partial read без err — на fragmented TCP это дало бы
// length из мусора и обрыв handshake.
func readFramed(conn net.Conn, maxSize int) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	size := int(lenBuf[0])<<8 | int(lenBuf[1])
	if size == 0 || size > maxSize {
		return nil, fmt.Errorf("invalid frame size: %d (max %d)", size, maxSize)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return buf, nil
}

func writeFramed(conn net.Conn, data []byte) error {
	if len(data) > 0xFFFF {
		return fmt.Errorf("frame too large: %d", len(data))
	}
	var lenBuf [2]byte
	lenBuf[0] = byte(len(data) >> 8)
	lenBuf[1] = byte(len(data))
	if _, err := conn.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}
