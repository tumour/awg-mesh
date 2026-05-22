package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flynn/noise"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// cmdServe — bootstrap-listener на seed-ноде.
//
// Принимает TCP-соединения на seed-endpoint'е, делает Noise_IKpsk2 handshake,
// валидирует cluster-secret через PSK, выделяет IP новой ноде, добавляет в
// state.peers и отвечает HelloResponse'ом с peer-list'ом.
//
// MVP: одна горутина на соединение, mutex на state-update.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	listenAddr := fs.String("listen", "",
		"bootstrap listen address (default :<state.listen_port>)")
	fs.Parse(args)

	s, err := state.Load(*stateFlag)
	if err != nil {
		return err
	}
	if !s.IsSeed {
		return fmt.Errorf("this node is not a seed (is_seed=false) — cannot serve bootstrap")
	}

	addr := *listenAddr
	if addr == "" {
		addr = fmt.Sprintf(":%d", s.ListenPort)
	}

	priv, err := wgkey.ParsePrivate(s.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	pub := priv.Public()
	cs, err := clusterkey.Parse(s.ClusterSecret)
	if err != nil {
		return fmt.Errorf("parse cluster secret: %w", err)
	}
	psk, err := handshake.DerivePSK(cs[:])
	if err != nil {
		return fmt.Errorf("derive psk: %w", err)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	defer listener.Close()

	log.Printf("meshd serve: listening on %s (label=%s, peers=%d)",
		addr, s.NodeLabel, len(s.Peers))

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	var stateMu sync.Mutex

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("meshd serve: shutting down")
				return nil
			}
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn, *stateFlag, priv, pub, psk, &stateMu)
	}
}

// handleConn — обработка одного bootstrap-join'а: Noise IK handshake →
// HelloRequest → state-update → HelloResponse.
func handleConn(
	conn net.Conn,
	statePath string,
	priv wgkey.Private,
	pub wgkey.Public,
	psk []byte,
	stateMu *sync.Mutex,
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
	msg1, err := readFramed(conn, 2048)
	if err != nil {
		log.Printf("[%s] read msg1: %v", remoteAddr, err)
		return
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		log.Printf("[%s] noise msg1 fail (wrong secret or wrong seed pubkey?): %v", remoteAddr, err)
		return
	}

	// Message 2 (server → client) + установка CipherState'ов.
	// flynn/noise возвращает (c_init_to_resp, c_resp_to_init) — порядок
	// одинаковый и в WriteMessage, и в ReadMessage. Сервер шифрует через
	// c_resp_to_init, расшифровывает через c_init_to_resp.
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
		_ = proto.WriteMessage(conn, csServerToClient, proto.HelloResponse{
			Version: proto.ProtoVersion,
			Status:  "error",
			Error: fmt.Sprintf("proto version mismatch (server=%d, client=%d)",
				proto.ProtoVersion, req.Version),
		})
		return
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	// Перечитываем state свежий — другая goroutine могла что-то добавить.
	s, err := state.Load(statePath)
	if err != nil {
		log.Printf("[%s] reload state: %v", remoteAddr, err)
		return
	}

	// Идемпотентно: если такой pubkey уже зарегистрирован, отдаём существующий IP.
	for _, p := range s.Peers {
		if p.PublicKey == req.PublicKey {
			log.Printf("[%s] peer %s already registered (ip=%s), returning existing",
				remoteAddr, req.Label, p.NodeIP)
			respondOK(conn, csServerToClient, s, p.NodeIP)
			return
		}
	}

	allocIP, err := allocateNextIP(s)
	if err != nil {
		log.Printf("[%s] alloc IP: %v", remoteAddr, err)
		_ = proto.WriteMessage(conn, csServerToClient, proto.HelloResponse{
			Version: proto.ProtoVersion,
			Status:  "error",
			Error:   fmt.Sprintf("IP allocation: %v", err),
		})
		return
	}

	s.Peers = append(s.Peers, state.Peer{
		Label:     req.Label,
		PublicKey: req.PublicKey,
		NodeIP:    allocIP,
		IsSeed:    false,
		LastSeen:  time.Now().UTC(),
	})
	if err := s.Save(statePath); err != nil {
		log.Printf("[%s] save state: %v", remoteAddr, err)
		return
	}

	log.Printf("[%s] registered peer %s as %s (pubkey=%s)",
		remoteAddr, req.Label, allocIP, shortKey(req.PublicKey))

	respondOK(conn, csServerToClient, s, allocIP)
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

// allocateNextIP — следующий свободный IP в state.NetworkCIDR.
func allocateNextIP(s *state.State) (string, error) {
	prefix, err := netip.ParsePrefix(s.NetworkCIDR)
	if err != nil {
		return "", fmt.Errorf("parse cidr: %w", err)
	}
	used := make(map[netip.Addr]bool)
	for _, p := range s.Peers {
		ip, err := netip.ParseAddr(p.NodeIP)
		if err == nil {
			used[ip] = true
		}
	}
	// .1 — обычно seed; начинаем с .2 для регулярных peer'ов.
	addr := prefix.Addr().Next().Next()
	for prefix.Contains(addr) {
		if !used[addr] && addr.As4()[3] != 255 {
			return addr.String(), nil
		}
		addr = addr.Next()
	}
	return "", fmt.Errorf("no free IPs in %s", s.NetworkCIDR)
}

func shortKey(k string) string {
	if len(k) > 12 {
		return k[:8] + "..."
	}
	return k
}

// readFramed читает 2-байт length prefix + body.
//
// ВАЖНО: используется io.ReadFull, не conn.Read — последний может вернуть
// partial read без err (например 1 байт вместо 2). На fragmented TCP это
// привело бы к length из мусора и обрыву handshake.
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
