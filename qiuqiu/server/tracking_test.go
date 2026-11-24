package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTrackingReceiptsAndIsolation(t *testing.T) {
	s, u, v := testServer(t)
	_, session, e := s.store.login(u.Name, "alice-test-password")
	if e != nil {
		t.Fatal(e)
	}
	_, other, e := s.store.login(v.Name, "bob-test-password")
	if e != nil {
		t.Fatal(e)
	}
	w := do(s, "POST", "/api/v1/manage/test", `{"device_ids":["DEV_a","DEV_b"],"title":"中文测试","body":"正文","priority":5,"business_type":"security","business_name":"登录审计","sound":"alarm","icon":"security","click_url":"https://example.com","tags":["test"]}`, session)
	if w.Code != 202 {
		t.Fatalf("test send: %d %s", w.Code, w.Body)
	}
	var result params
	json.Unmarshal(w.Body.Bytes(), &result)
	id := result.str("message_id")
	if id == "" {
		t.Fatal("no test id")
	}
	if w = do(s, "GET", "/api/v1/manage/messages/"+id, "", other); w.Code != 404 {
		t.Fatalf("cross-user snapshot %d", w.Code)
	}
	receipt := encode(params{"receipts": []params{{"message_id": id, "stage": "received"}, {"message_id": id, "stage": "notification_blocked", "reason": "权限关闭"}}})
	for i := 0; i < 2; i++ {
		if w = do(s, "POST", "/api/v1/device/receipts", receipt, "DEV_a"); w.Code != 200 {
			t.Fatalf("receipt %d %s", w.Code, w.Body)
		}
	}
	if w = do(s, "POST", "/api/v1/device/receipts", receipt, "DEV_c"); w.Code != 200 || !strings.Contains(w.Body.String(), "unknown_message") {
		t.Fatalf("cross-device receipt %d", w.Code)
	}
	w = do(s, "GET", "/api/v1/manage/messages/"+id, "", session)
	var snapshot struct {
		Events     []params `json:"events"`
		Receipts   []params `json:"receipts"`
		Deliveries []params `json:"deliveries"`
		Message    Message  `json:"message"`
	}
	json.Unmarshal(w.Body.Bytes(), &snapshot)
	if len(snapshot.Receipts) != 2 || len(snapshot.Deliveries) != 2 || len(snapshot.Events) != 5 {
		t.Fatalf("unexpected snapshot: %s", w.Body)
	}
	if !snapshot.Message.Test || snapshot.Message.Priority != 5 || snapshot.Message.BusinessName != "登录审计" {
		t.Fatalf("lost test fields: %+v", snapshot.Message)
	}
	if w = do(s, "POST", "/api/v1/device/receipts", `{"receipts":[{"message_id":"missing","stage":"received"}]}`, "DEV_a"); w.Code != 200 || !strings.Contains(w.Body.String(), "unknown_message") {
		t.Fatalf("unknown message %d", w.Code)
	}
	if w = do(s, "POST", "/api/v1/device/receipts", `{"receipts":[{"message_id":"missing","stage":"fake_success"}]}`, "DEV_a"); w.Code != 400 {
		t.Fatalf("fake stage %d", w.Code)
	}
	s.store.Exec("UPDATE devices SET disabled=1 WHERE id='DEV_a'")
	if w = do(s, "POST", "/api/v1/device/receipts", receipt, "DEV_a"); w.Code != 401 {
		t.Fatalf("revoked %d", w.Code)
	}
}

func TestFriendlyDeviceRename(t *testing.T) {
	s, u, v := testServer(t)
	_, token, _ := s.store.login(u.Name, "alice-test-password")
	_, other, _ := s.store.login(v.Name, "bob-test-password")
	if w := do(s, "PATCH", "/api/v1/manage/devices/profile", `{"id":"DEV_a","name":"我的 Find N5","alias":"phone"}`, token); w.Code != 200 {
		t.Fatalf("rename %d %s", w.Code, w.Body)
	}
	if w := do(s, "PATCH", "/api/v1/manage/devices/profile", `{"id":"DEV_a","name":"越权"}`, other); w.Code != 404 {
		t.Fatalf("isolation %d", w.Code)
	}
	if w := do(s, "PATCH", "/api/v1/manage/devices/profile", `{"id":"DEV_c","name":"另一个手机","alias":"phone"}`, other); w.Code != 200 {
		t.Fatalf("tenant alias %d %s", w.Code, w.Body)
	}
}

func TestEventStreamCursorAndTenantIsolation(t *testing.T) {
	s, u, v := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	key := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	sent := do(s, "POST", "/api/v1/push", `{"body":"SSE测试"}`, key)
	var result params
	json.Unmarshal(sent.Body.Bytes(), &result)
	id := result.str("message_id")
	var cursor int64
	s.store.QueryRow("SELECT max(id) FROM delivery_events WHERE user_id=?", u.ID).Scan(&cursor)
	// An event from another tenant after the resume cursor must remain invisible.
	other := issued(t, s, v, "native", Targets{Devices: []string{"DEV_c"}})
	do(s, "POST", "/api/v1/push", `{"body":"private"}`, other)
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/manage/events", nil)
	req.Header.Set("Authorization", "Bearer "+session)
	req.Header.Set("Last-Event-ID", fmt.Sprint(cursor))
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("not SSE")
	}
	ack := do(s, "POST", "/api/v1/device/receipts", encode(params{"receipts": []params{{"message_id": id, "stage": "received"}}}), "DEV_a")
	if ack.Code != 200 {
		t.Fatalf("receipt %d", ack.Code)
	}
	scan := bufio.NewScanner(response.Body)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event params
		json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event)
		if event.str("user_id") != u.ID || event.str("message_id") != id || event.str("stage") != "received" {
			t.Fatalf("cursor/isolation failed: %v", event)
		}
		return
	}
	t.Fatalf("no receipt event: %v", scan.Err())
}
