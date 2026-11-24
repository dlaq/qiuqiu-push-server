package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestRouteFallbackAndEncryptedConfig(t *testing.T) {
	s, u, v := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w, 200, params{"id": "ntfy_test"}) }))
	defer ntfy.Close()
	s.cfg.NtfyURL = ntfy.URL
	channel := func(kind string) string {
		w := do(s, "POST", "/api/v1/manage/channels", encode(params{"name": kind, "kind": kind, "config": params{"token": "super-secret-value"}}), session)
		if w.Code != 201 {
			t.Fatalf("channel: %d %s", w.Code, w.Body)
		}
		var p params
		json.Unmarshal(w.Body.Bytes(), &p)
		return p.str("id")
	}
	fcm, store := channel("ntfy_fcm"), channel("store")
	var raw string
	s.store.QueryRow("SELECT config FROM outbound_channels WHERE id=?", fcm).Scan(&raw)
	if strings.Contains(raw, "super-secret-value") {
		t.Fatal("plaintext credentials")
	}
	w := do(s, "POST", "/api/v1/manage/policies", encode(params{"name": "fallback", "mode": "fallback", "steps": []params{{"channel_id": fcm, "timeout_seconds": 60}, {"channel_id": store, "timeout_seconds": 60}}}), session)
	if w.Code != 201 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
	var p params
	json.Unmarshal(w.Body.Bytes(), &p)
	policy := p.str("id")
	w = do(s, "POST", "/api/v1/manage/test", encode(params{"device_id": "DEV_a", "body": "测试", "delivery_policy": policy}), session)
	if w.Code != 202 {
		t.Fatalf("send %d %s", w.Code, w.Body)
	}
	if e := s.deliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	var stage string
	s.store.QueryRow("SELECT state FROM route_attempts ORDER BY id LIMIT 1").Scan(&stage)
	if stage != "waiting_receipt" {
		t.Fatalf("stage %s", stage)
	}
	s.store.Exec("UPDATE route_attempts SET deadline=?", time.Now().Add(-time.Second).Unix())
	if e := s.deliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	var count int
	s.store.QueryRow("SELECT count(*) FROM route_attempts WHERE state IN ('receipt_timeout','stored')").Scan(&count)
	if count != 2 {
		t.Fatalf("fallback missing %d", count)
	}
	if e := s.checkPolicy(v.ID, &Message{Policy: policy}); e == nil {
		t.Fatal("cross-tenant policy authorized")
	}
	list := do(s, "GET", "/api/v1/manage/channels", "", session)
	if strings.Contains(list.Body.String(), "super-secret-value") || strings.Contains(list.Body.String(), "config") {
		t.Fatal("configuration leaked")
	}
}

func TestWebsocketPublisherAndDirect(t *testing.T) {
	s, u, _ := testServer(t)
	server := httptest.NewServer(s)
	defer server.Close()
	key := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	pub, e := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/ws", "", server.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer pub.Close()
	pub.SetDeadline(time.Now().Add(5 * time.Second))
	websocket.JSON.Send(pub, params{"token": key})
	var frame params
	if e = websocket.JSON.Receive(pub, &frame); e != nil || frame.str("event") != "authenticated" {
		t.Fatalf("auth: %v %v", frame, e)
	}
	websocket.JSON.Send(pub, params{"id": "r1", "body": params{"body": "WS 中文消息", "business_name": "直连测试"}})
	if e = websocket.JSON.Receive(pub, &frame); e != nil || frame.num("status") != 202 {
		t.Fatalf("publish: %v %v", frame, e)
	}
	device, e := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/device/ws", "", server.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer device.Close()
	device.SetDeadline(time.Now().Add(5 * time.Second))
	websocket.JSON.Send(device, params{"token": "DEV_a"})
	if e = websocket.JSON.Receive(device, &frame); e != nil || frame.str("event") != "authenticated" {
		t.Fatalf("device auth: %v %v", frame, e)
	}
	do(s, "POST", "/api/v1/device/profile", `{"mode":"direct","app_version":"test"}`, "DEV_a")
	if e = s.deliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = websocket.JSON.Receive(device, &frame); e != nil || frame.str("message") != "WS 中文消息" {
		t.Fatalf("receive %v %v", frame, e)
	}
	websocket.JSON.Send(pub, params{"id": "bad", "path": "/api/v1/manage/users", "body": params{}})
	websocket.JSON.Receive(pub, &frame)
	if frame.num("status") != 400 {
		t.Fatal("WS management route exposed")
	}
}

func TestOneBotWebsocketRequiresValidAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		accepted   bool
	}{{"ok", "0", true}, {"invalid", `"not-an-integer"`, false}, {"null", "null", false}, {"underflow", "1e-999", false}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
				defer ws.Close()
				var req params
				if websocket.JSON.Receive(ws, &req) != nil {
					return
				}
				websocket.JSON.Send(ws, params{"echo": req.str("echo"), "status": "ok", "retcode": json.RawMessage(tc.code)})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := sendOneBotWS(ctx, ChannelConfig{"url": "ws" + strings.TrimPrefix(server.URL, "http"), "allow_private": "true"}, params{"message": "测试"})
			if (err == nil) != tc.accepted {
				t.Fatalf("acceptance=%v error=%v", tc.accepted, err)
			}
		})
	}
}
