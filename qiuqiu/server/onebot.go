package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"golang.org/x/net/websocket"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

func sendOneBotWS(ctx context.Context, c ChannelConfig, body any) error {
	u, e := url.Parse(c["url"])
	if e != nil {
		return e
	}
	if u.Scheme != "wss" && u.Scheme != "ws" {
		return bad("OneBot WebSocket地址无效")
	}
	ips, e := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if e != nil {
		return e
	}
	if c["allow_private"] != "true" {
		for _, ip := range ips {
			if ip.IP.IsPrivate() || !ip.IP.IsGlobalUnicast() || ip.IP.IsLoopback() {
				return bad("OneBot私网地址需要管理员显式允许")
			}
		}
	}
	cfg, e := websocket.NewConfig(c["url"], "https://qiuqiu.local")
	if e != nil {
		return e
	}
	cfg.Dialer = &net.Dialer{Timeout: 10 * time.Second, ControlContext: func(_ context.Context, _ string, address string, _ syscall.RawConn) error {
		if c["allow_private"] == "true" {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsPrivate() || !ip.IsGlobalUnicast() || ip.IsLoopback() {
			return bad("OneBot连接目标不是公网地址")
		}
		return nil
	}}
	cfg.TlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	cfg.Header = http.Header{}
	cfg.Header.Set("Authorization", "Bearer "+c["token"])
	ws, e := cfg.DialContext(ctx)
	if e != nil {
		return fmt.Errorf("OneBot WS连接失败")
	}
	defer ws.Close()
	_ = ws.SetDeadline(time.Now().Add(15 * time.Second))
	ws.MaxPayloadBytes = 1 << 20
	echo := uuid()
	if e = websocket.JSON.Send(ws, params{"action": "send_msg", "params": body, "echo": echo}); e != nil {
		return e
	}
	for {
		var response params
		if e = websocket.JSON.Receive(ws, &response); e != nil {
			return e
		}
		if response.str("echo") == echo {
			if response.str("status") != "ok" || response.num("retcode") != 0 {
				return fmt.Errorf("OneBot拒绝发送")
			}
			return nil
		}
	}
}

func (s *Server) oneBotReverseHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/onebot/reverse/")
	var uid string
	if e := s.store.QueryRow("SELECT c.user_id FROM outbound_channels c JOIN users u ON u.id=c.user_id WHERE c.id=? AND c.kind='onebot' AND c.disabled=0 AND u.disabled=0", id).Scan(&uid); e != nil {
		fail(w, r, forbidden())
		return
	}
	_, c, e := s.readChannel(uid, id)
	if e != nil || c["token"] == "" || digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != digest(c["token"]) {
		fail(w, r, forbidden())
		return
	}
	websocket.Server{Handshake: func(_ *websocket.Config, _ *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		ws.MaxPayloadBytes = 1 << 20
		client := &directClient{Send: make(chan directPacket, 32), Closed: make(chan struct{})}
		s.directMu.Lock()
		s.externalClients[id] = client
		s.directMu.Unlock()
		defer func() {
			close(client.Closed)
			s.directMu.Lock()
			if s.externalClients[id] == client {
				delete(s.externalClients, id)
			}
			s.directMu.Unlock()
		}()
		done := make(chan struct{})
		responses := make(chan params, 16)
		go func() {
			defer close(done)
			for {
				_ = ws.SetReadDeadline(time.Now().Add(120 * time.Second))
				var frame params
				if websocket.JSON.Receive(ws, &frame) != nil {
					return
				}
				if frame.str("echo") != "" {
					select {
					case responses <- frame:
					default:
					}
				}
			}
		}()
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if _, _, e = s.readChannel(uid, id); e != nil {
					return
				}
			case p := <-client.Send:
				echo := uuid()
				_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := websocket.JSON.Send(ws, params{"action": "send_msg", "params": p.Payload, "echo": echo}); err != nil {
					p.Done <- err
					return
				}
				timer := time.NewTimer(12 * time.Second)
				matched := false
				for !matched {
					select {
					case response := <-responses:
						if response.str("echo") == echo {
							var err error
							if response.str("status") != "ok" || response.num("retcode") != 0 {
								err = fmt.Errorf("OneBot拒绝发送")
							}
							p.Done <- err
							matched = true
						}
					case <-timer.C:
						p.Done <- fmt.Errorf("OneBot回执超时")
						matched = true
					case <-done:
						timer.Stop()
						p.Done <- fmt.Errorf("OneBot断开")
						return
					}
				}
				timer.Stop()
			}
		}
	}}.ServeHTTP(w, r)
}

func (s *Server) sendOneBotReverse(ctx context.Context, c ChannelConfig, body any) error {
	s.directMu.Lock()
	client := s.externalClients[c["_id"]]
	s.directMu.Unlock()
	if client == nil {
		return fmt.Errorf("OneBot反向连接未建立")
	}
	p := directPacket{Payload: body, Done: make(chan error, 1)}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case client.Send <- p:
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("OneBot发送超时")
	case <-client.Closed:
		return fmt.Errorf("OneBot连接已断开")
	}
	select {
	case e := <-p.Done:
		return e
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("OneBot回执超时")
	case <-client.Closed:
		return fmt.Errorf("OneBot连接已断开")
	}
}
