package main

import (
	"context"
	"errors"
	"golang.org/x/net/websocket"
	"net/http"
	"strings"
	"time"
)

func init() {
	channelNames["client"] = "message-pusher WebSocket客户端"
	channelNames["tencent_alarm"] = "腾讯云旧告警（接口已删除）"
}

func (s *Server) legacyClientHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/register_client/")
	key := r.URL.Query().Get("secret")
	if key == "" {
		fail(w, r, &AppError{401, "unauthorized", "需要独立客户端连接密钥"})
		return
	}
	var uid string
	if e := s.store.QueryRow("SELECT id FROM users WHERE name=? AND disabled=0", name).Scan(&uid); e != nil {
		fail(w, r, forbidden())
		return
	}
	rows, e := s.store.list("SELECT id FROM outbound_channels WHERE user_id=? AND kind='client' AND disabled=0", uid)
	if e != nil {
		fail(w, r, e)
		return
	}
	channel := ""
	for _, row := range rows {
		_, c, err := s.readChannel(uid, row["id"].(string))
		if err == nil && digest(c["secret"]) == digest(key) {
			channel = row["id"].(string)
			break
		}
	}
	if channel == "" {
		fail(w, r, forbidden())
		return
	}
	websocket.Server{Handshake: func(_ *websocket.Config, _ *http.Request) error { return nil }, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		ws.MaxPayloadBytes = 1 << 20
		client := &directClient{Send: make(chan directPacket, 32), Closed: make(chan struct{})}
		s.directMu.Lock()
		s.externalClients[channel] = client
		s.directMu.Unlock()
		defer func() {
			close(client.Closed)
			s.directMu.Lock()
			if s.externalClients[channel] == client {
				delete(s.externalClients, channel)
			}
			s.directMu.Unlock()
		}()
		done := make(chan struct{})
		incoming := make(chan string, 8)
		go func() {
			defer close(done)
			for {
				_ = ws.SetReadDeadline(time.Now().Add(120 * time.Second))
				var frame string
				if websocket.Message.Receive(ws, &frame) != nil {
					return
				}
				select {
				case incoming <- frame:
				default:
				}
			}
		}()
		ticker := time.NewTicker(56 * time.Second)
		defer ticker.Stop()
		for {
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			select {
			case <-done:
				return
			case frame := <-incoming:
				if frame == "ping" {
					if websocket.Message.Send(ws, "pong") != nil {
						return
					}
				}
			case p := <-client.Send:
				err := websocket.JSON.Send(ws, p.Payload)
				p.Done <- err
				if err != nil {
					return
				}
			case <-ticker.C:
				if _, _, err := s.readChannel(uid, channel); err != nil {
					return
				}
				s.directMu.Lock()
				same := s.externalClients[channel] == client
				s.directMu.Unlock()
				if !same {
					return
				}
				if websocket.Message.Send(ws, "ping") != nil {
					return
				}
			}
		}
	}}.ServeHTTP(w, r)
}

func (s *Server) sendLegacyClient(ctx context.Context, c ChannelConfig, m Message) error {
	s.directMu.Lock()
	client := s.externalClients[c["_id"]]
	s.directMu.Unlock()
	if client == nil {
		return errors.New("WebSocket客户端未连接")
	}
	p := directPacket{Payload: params{"title": m.Title, "description": m.Body, "content": m.Body, "html_content": htmlPolicy.Sanitize(m.Body), "url": m.Click, "message_id": m.ID}, Done: make(chan error, 1)}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	select {
	case client.Send <- p:
	case <-client.Closed:
		return errors.New("客户端已断开")
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout.C:
		return errors.New("客户端发送超时")
	}
	select {
	case err := <-p.Done:
		return err
	case <-client.Closed:
		return errors.New("客户端已断开")
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout.C:
		return errors.New("客户端发送超时")
	}
}
