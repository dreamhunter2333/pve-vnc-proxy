package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/netutil"
	"golang.org/x/net/websocket"
)

const handshakeTimeout = 15 * time.Second

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.Listen, err)
	}
	ln = netutil.LimitListener(ln, cfg.MaxConns)
	log.Printf("pve-vnc-proxy listening on %s -> %s (max-conns=%d, insecure=%v)",
		cfg.Listen, cfg.Host, cfg.MaxConns, cfg.Insecure)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn, cfg)
	}
}

func handleConn(c net.Conn, cfg *Config) {
	defer c.Close()
	remote := c.RemoteAddr().String()

	c.SetDeadline(time.Now().Add(handshakeTimeout))

	route, err := handshakeClient(c)
	if err != nil {
		log.Printf("[%s] client handshake: %v", remote, err)
		return
	}
	log.Printf("[%s] route node=%s vmid=%d", remote, route.Node, route.VMID)

	sess, err := requestVNCProxy(cfg.Host, route.TokenID, route.Secret, route.Node, route.VMID, cfg.Insecure)
	if err != nil {
		writeSecResult(c, friendlyAPIError(err))
		log.Printf("[%s] vncproxy: %v", remote, err)
		return
	}

	ws, err := dialPVEWebSocket(cfg.Host, route.Node, route.VMID, route.TokenID, route.Secret, sess, cfg.Insecure)
	if err != nil {
		writeSecResult(c, "wss dial failed")
		log.Printf("[%s] wss dial: %v", remote, err)
		return
	}
	defer ws.Close()

	if err := handshakeUpstream(ws, sess.Ticket); err != nil {
		writeSecResult(c, "upstream auth failed")
		log.Printf("[%s] upstream handshake: %v", remote, err)
		return
	}

	if err := finishClientAuthOK(c); err != nil {
		log.Printf("[%s] auth ok write: %v", remote, err)
		return
	}

	c.SetDeadline(time.Time{})

	log.Printf("[%s] tunnel established node=%s vmid=%d", remote, route.Node, route.VMID)
	pipe(c, ws)
	log.Printf("[%s] tunnel closed", remote)
}

func friendlyAPIError(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "vncproxy http "); i >= 0 {
		return strings.TrimSpace(msg[i:])
	}
	return "vncproxy api failed"
}

func dialPVEWebSocket(host, node string, vmid int, tokenID, secret string, s *vncSession, insecure bool) (*websocket.Conn, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, err
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}

	wsURL := fmt.Sprintf("%s://%s/api2/json/nodes/%s/qemu/%d/vncwebsocket?port=%s&vncticket=%s",
		scheme, u.Host, url.PathEscape(node), vmid,
		url.QueryEscape(s.Port), url.QueryEscape(s.Ticket))

	wsCfg, err := websocket.NewConfig(wsURL, host)
	if err != nil {
		return nil, err
	}
	wsCfg.TlsConfig = &tls.Config{InsecureSkipVerify: insecure}
	wsCfg.Header = http.Header{
		"Authorization": []string{"PVEAPIToken=" + tokenID + "=" + secret},
	}
	wsCfg.Dialer = &net.Dialer{Timeout: handshakeTimeout}

	return websocket.DialConfig(wsCfg)
}

func pipe(a net.Conn, b *websocket.Conn) {
	b.PayloadType = websocket.BinaryFrame

	var wg sync.WaitGroup
	wg.Add(2)

	closeBoth := func() {
		a.Close()
		b.Close()
	}

	go func() {
		defer wg.Done()
		_, err := io.Copy(b, a)
		if err != nil && !isClosedConn(err) {
			log.Printf("copy a->b: %v", err)
		}
		closeBoth()
	}()
	go func() {
		defer wg.Done()
		_, err := io.Copy(a, b)
		if err != nil && !isClosedConn(err) {
			log.Printf("copy b->a: %v", err)
		}
		closeBoth()
	}()

	wg.Wait()
}

func isClosedConn(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}
