package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

type directPacket struct {
	Payload any
	Done    chan error
}
type directClient struct {
	Send   chan directPacket
	Closed chan struct{}
}

func (s *Server) websocketHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "WebSocket requires GET", 405)
		return
	}
	server := websocket.Server{Handshake: func(cfg *websocket.Config, req *http.Request) error {
		// Native clients omit Origin. Browser clients must use the same host.
		if raw := req.Header.Get("Origin"); raw != "" {
			u, e := url.Parse(raw)
			if e != nil || u.Host != req.Host {
				return errors.New("origin denied")
			}
		}
		return nil
	}, Handler: func(ws *websocket.Conn) {
		defer ws.Close()
		ws.MaxPayloadBytes = 1 << 20
		_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		var auth struct {
			Token string `json:"token"`
		}
		if websocket.JSON.Receive(ws, &auth) != nil || len(auth.Token) > 256 {
			return
		}
		if r.URL.Path == "/api/v1/device/ws" {
			s.deviceSocket(ws, auth.Token)
			return
		}
		s.publisherSocket(ws, auth.Token)
	}}
	server.ServeHTTP(w, r)
}

func (s *Server) publisherSocket(ws *websocket.Conn, token string) {
	// A send key is mandatory; a management session never authorizes publishing.
	var valid int
	if s.store.QueryRow("SELECT count(*) FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.hash=? AND k.disabled=0 AND u.disabled=0", digest(token)).Scan(&valid) != nil || valid != 1 {
		_ = websocket.JSON.Send(ws, params{"error": "unauthorized"})
		return
	}
	_ = websocket.JSON.Send(ws, params{"event": "authenticated", "extension": "qiuqiu.ws.v1"})
	for {
		_ = ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		var p struct {
			ID          string          `json:"id"`
			Path        string          `json:"path"`
			Body        json.RawMessage `json:"body"`
			Idempotency string          `json:"idempotency_key"`
			Event       string          `json:"event"`
		}
		if websocket.JSON.Receive(ws, &p) != nil {
			return
		}
		if p.Event == "ping" {
			if websocket.JSON.Send(ws, params{"event": "pong"}) != nil {
				return
			}
			continue
		}
		path := first(p.Path, "/api/v1/push")
		allowed := path == "/ntfy" || path == "/api/v1/push" || path == "/send" || path == "/push" || path == "/api/send/message" || path == "/pushbear/sub" || strings.HasPrefix(path, "/sctapi/") || strings.HasPrefix(path, "/sc3/") || strings.HasPrefix(path, "/bark/") || strings.HasPrefix(path, "/send/")
		if !allowed || strings.ContainsAny(path, "?#\r\n") {
			if websocket.JSON.Send(ws, params{"id": p.ID, "status": 400, "error": "invalid_path"}) != nil {
				return
			}
			continue
		}
		req, err := http.NewRequest("POST", path, bytes.NewReader(p.Body))
		if err != nil {
			if websocket.JSON.Send(ws, params{"id": p.ID, "status": 400, "error": "invalid_path"}) != nil {
				return
			}
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if path == "/ntfy" {
			req.Host = s.cfg.NtfyHost
			req.URL.Path = "/"
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", p.Idempotency)
		recorder := httptest.NewRecorder()
		s.ServeHTTP(recorder, req)
		var response any
		if json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
			response = params{"error": "invalid_response"}
		}
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if websocket.JSON.Send(ws, params{"id": p.ID, "status": recorder.Code, "response": response}) != nil {
			return
		}
	}
}

func directWire(m Message, topic string) params {
	meta := params{"message_id": m.ID, "business_type": m.Business, "business_name": m.BusinessName, "icon": m.Icon, "sound": m.Sound, "content_type": m.ContentType, "source_protocol": m.Protocol, "group": m.Group}
	wire := params{"id": "direct_" + m.ID, "event": "message", "topic": topic, "time": m.CreatedAt, "sequence_id": first(m.Sequence, m.ID), "title": m.Title, "message": m.Body, "priority": m.Priority, "tags": m.Tags, "click": m.Click, "qiuqiu": meta}
	if m.IconURL != "" {
		wire["icon"] = m.IconURL
	}
	if m.ContentType == "markdown" {
		wire["content_type"] = "text/markdown"
	}
	for _, field := range []string{"actions", "icon", "attachment", "encoding"} {
		if value, ok := m.Ntfy[field]; ok {
			wire[field] = value
		}
	}
	return wire
}

func (s *Server) deviceSocket(ws *websocket.Conn, token string) {
	d, e := s.deviceAuth(token)
	if e != nil {
		_ = websocket.JSON.Send(ws, params{"error": "unauthorized"})
		return
	}
	client := &directClient{Send: make(chan directPacket, 32), Closed: make(chan struct{})}
	s.directMu.Lock()
	s.direct[d.ID] = client
	s.directMu.Unlock()
	defer func() {
		close(client.Closed)
		s.directMu.Lock()
		if s.direct[d.ID] == client {
			delete(s.direct, d.ID)
		}
		s.directMu.Unlock()
	}()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			_ = ws.SetReadDeadline(time.Now().Add(90 * time.Second))
			var frame params
			if websocket.JSON.Receive(ws, &frame) != nil {
				return
			}
		}
	}()
	send := func(v any) error {
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return websocket.JSON.Send(ws, v)
	}
	if send(params{"event": "authenticated", "device_id": d.ID}) != nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	rows, e := s.store.list("SELECT m.payload FROM deliveries v JOIN messages m ON m.id=v.message_id WHERE v.device_id=? AND (v.status='published' OR v.status='pending' AND EXISTS(SELECT 1 FROM route_attempts a WHERE a.delivery_id=v.id AND a.state='waiting_receipt')) AND NOT EXISTS(SELECT 1 FROM receipts r WHERE r.message_id=v.message_id AND r.device_id=v.device_id AND r.stage='received') ORDER BY v.id DESC LIMIT 1000", d.ID)
	if e != nil {
		return
	}
	for _, row := range rows {
		var m Message
		if json.Unmarshal([]byte(row["payload"].(string)), &m) == nil && m.CreatedAt >= time.Now().Add(-168*time.Hour).Unix() && (m.Expires == 0 || m.Expires > time.Now().Unix()) {
			if send(directWire(m, d.Inbox)) != nil {
				return
			}
		}
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-closed:
			return
		case packet := <-client.Send:
			err := send(packet.Payload)
			packet.Done <- err
			if err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := s.deviceAuth(token); err != nil {
				return
			}
			s.directMu.Lock()
			active := s.direct[d.ID] == client
			s.directMu.Unlock()
			if !active {
				return
			}
			if send(params{"event": "ping"}) != nil {
				return
			}
		}
	}
}

func (s *Server) sendDirect(ctx context.Context, device string, m Message, topic string) error {
	s.directMu.Lock()
	client := s.direct[device]
	s.directMu.Unlock()
	if client == nil {
		return errors.New("设备直连未连接")
	}
	packet := directPacket{Payload: directWire(m, topic), Done: make(chan error, 1)}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	select {
	case client.Send <- packet:
	case <-client.Closed:
		return errors.New("设备直连已断开")
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout.C:
		return errors.New("设备直连发送超时")
	}
	select {
	case e := <-packet.Done:
		return e
	case <-client.Closed:
		return errors.New("设备直连已断开")
	case <-ctx.Done():
		return ctx.Err()
	case <-timeout.C:
		return errors.New("设备直连发送超时")
	}
}

func (s *Server) dispatchDevice(ctx context.Context, device string, m Message, topic string) (string, error) {
	var mode string
	_ = s.store.QueryRow("SELECT mode FROM device_profiles WHERE device_id=?", device).Scan(&mode)
	if mode == "direct" || mode == "auto" {
		err := s.sendDirect(ctx, device, m, topic)
		stage := "direct_sent"
		detail := ""
		if err != nil {
			stage = "direct_unavailable"
			detail = "设备直连不可用"
		}
		_, _ = s.store.Exec("INSERT INTO delivery_events(user_id,message_id,device_id,channel,stage,created_at,detail) VALUES(?,?,?,?,?,?,?)", m.UserID, m.ID, device, "app_direct", stage, time.Now().Unix(), detail)
		if err == nil {
			return "direct_" + m.ID, nil
		}
		if mode == "direct" {
			return "", err
		}
	}
	return s.publish(ctx, m, topic)
}

func (s *Server) autoFallbacks(ctx context.Context) error {
	rows, e := s.store.list(`SELECT v.id,v.device_id,v.topic,m.payload FROM deliveries v JOIN messages m ON m.id=v.message_id JOIN device_profiles p ON p.device_id=v.device_id JOIN devices d ON d.id=v.device_id JOIN users u ON u.id=d.user_id WHERE p.mode='auto' AND d.disabled=0 AND u.disabled=0 AND v.ntfy_id LIKE 'direct_%' AND v.status='published' AND NOT EXISTS(SELECT 1 FROM receipts r WHERE r.message_id=v.message_id AND r.device_id=v.device_id AND r.stage='received') AND EXISTS(SELECT 1 FROM delivery_events e WHERE e.message_id=v.message_id AND e.device_id=v.device_id AND e.stage='direct_sent' AND e.created_at<=?) AND NOT EXISTS(SELECT 1 FROM route_attempts a WHERE a.delivery_id=v.id AND a.step=-1) LIMIT 50`, time.Now().Add(-60*time.Second).Unix())
	if e != nil {
		return e
	}
	for _, row := range rows {
		var m Message
		if e = json.Unmarshal([]byte(row["payload"].(string)), &m); e != nil {
			return e
		}
		if m.Expires > 0 && m.Expires <= time.Now().Unix() {
			continue
		}
		_, e = s.store.Exec("INSERT INTO route_attempts(delivery_id,channel_id,step,state,started_at,deadline,detail) VALUES(?,'ntfy_fcm',-1,'sending',?,?,'直连60秒内未收到回执，切换FCM')", row["id"], time.Now().Unix(), time.Now().Unix()+60)
		if e != nil {
			return e
		}
		transportID, err := s.publish(ctx, m, row["topic"].(string))
		if err == nil {
			_, err = s.store.Exec("INSERT OR IGNORE INTO transport_ids VALUES(?,?,?,?)", row["id"], "ntfy_fcm", transportID, time.Now().Unix())
		}
		state, detail := "waiting_receipt", "ntfy已受理备用投递，等待设备回执"
		if err != nil {
			state = "failed"
			detail = "备用FCM发布失败"
		}
		if _, e = s.store.Exec("UPDATE route_attempts SET state=?,detail=? WHERE delivery_id=? AND step=-1", state, detail, row["id"]); e != nil {
			return e
		}
	}
	return nil
}
