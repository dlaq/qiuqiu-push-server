package main

import (
	"bytes"
	"context"
	"encoding/json"
	"golang.org/x/net/websocket"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func accessRequest(s *Server, method, path, body, access string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("access-key", access)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestPushPlusMessageLifecycleIsolation(t *testing.T) {
	s, u, v := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	_, otherSession, _ := s.store.login(v.Name, "bob-test-password")
	k, e := s.store.issueKey(u.ID, "", "pushplus", "open", Targets{Devices: []string{"DEV_a"}})
	if e != nil {
		t.Fatal(e)
	}
	token := k["token"].(string)
	secretKey := strings.Repeat("S", 32)
	config := encode(params{"id": k["id"], "secret": secretKey, "enabled": true})
	if w := do(s, "POST", "/api/v1/manage/keys/access", config, otherSession); w.Code != 403 {
		t.Fatalf("other config %d", w.Code)
	}
	if w := do(s, "POST", "/api/common/openApi/getAccessKey", encode(params{"token": token, "secretKey": secretKey}), ""); w.Code != 401 {
		t.Fatalf("not opt in %d", w.Code)
	}
	if w := do(s, "POST", "/api/v1/manage/keys/access", config, session); w.Code != 200 {
		t.Fatalf("config %d %s", w.Code, w.Body)
	}
	getAccess := func() string {
		t.Helper()
		w := do(s, "POST", "/api/common/openApi/getAccessKey", encode(params{"token": token, "secretKey": secretKey}), "")
		if w.Code != 200 {
			t.Fatalf("access %d %s", w.Code, w.Body)
		}
		var p struct {
			Data struct {
				Access string `json:"accessKey"`
			}
		}
		if e := json.Unmarshal(w.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		return p.Data.Access
	}
	access := getAccess()
	newAccess := getAccess()
	var expiry int64
	s.store.QueryRow("SELECT expires FROM compat_access_tokens WHERE hash=?", digest(access)).Scan(&expiry)
	if expiry > time.Now().Unix()+300 {
		t.Fatal("old key has no rotation grace cap")
	}
	var storedSecret string
	s.store.QueryRow("SELECT secret_hash FROM compat_access_settings WHERE key_id=?", k["id"]).Scan(&storedSecret)
	if storedSecret == secretKey || storedSecret != digest(secretKey) {
		t.Fatal("secret not hashed")
	}
	send := func(token string) string {
		t.Helper()
		w := do(s, "POST", "/send", encode(params{"token": token, "title": "中文", "content": "正文"}), "")
		if w.Code != 200 {
			t.Fatalf("send %d %s", w.Code, w.Body)
		}
		var p struct{ Data string }
		json.Unmarshal(w.Body.Bytes(), &p)
		if p.Data == "" {
			t.Fatal("data must be shortCode string")
		}
		return p.Data
	}
	id := send(token)
	sibling := issued(t, s, u, "pushplus", Targets{Devices: []string{"DEV_b"}})
	hidden := send(sibling)
	w := accessRequest(s, "POST", "/api/open/message/list", `{}`, access)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) || strings.Contains(w.Body.String(), hidden) {
		t.Fatalf("list scope %d %s", w.Code, w.Body)
	}
	if w = accessRequest(s, "GET", "/api/open/message/sendMessageResult?shortCode="+hidden, "", access); w.Code != 404 {
		t.Fatal("same-user sibling messages leaked")
	}
	if w = accessRequest(s, "POST", "/api/open/message/list", `{"pageSize":51}`, access); w.Code != 400 {
		t.Fatal("unbounded page")
	}
	w = accessRequest(s, "GET", "/api/open/message/sendMessageResult?shortCode="+id, "", access)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":0`) {
		t.Fatalf("pending %s", w.Body)
	}
	s.store.Exec("UPDATE deliveries SET status='published',attempts=1 WHERE message_id=?", id)
	w = accessRequest(s, "GET", "/api/open/message/sendMessageResult?shortCode="+id, "", access)
	if !strings.Contains(w.Body.String(), `"status":2`) || !strings.Contains(w.Body.String(), `"received_devices":0`) {
		t.Fatal("publication falsely claims phone reception")
	}
	if w = do(s, "GET", "/shortMessage/"+id, "", ""); w.Code != 200 || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("content %d", w.Code)
	}
	if w = accessRequest(s, "DELETE", "/api/open/message/deleteMessage?shortCode="+id, "", access); w.Code != 200 {
		t.Fatalf("delete %s", w.Body)
	}
	if w = do(s, "GET", "/shortMessage/"+id, "", ""); w.Code != 410 {
		t.Fatal("deleted content still public")
	}
	var count int
	s.store.QueryRow("SELECT count(*) FROM messages WHERE id=?", id).Scan(&count)
	if count != 1 {
		t.Fatal("audit record removed")
	}
	if w = accessRequest(s, "POST", "/api/open/message/list", `{}`, access); strings.Contains(w.Body.String(), id) {
		t.Fatal("deleted message listed")
	}
	s.store.Exec("UPDATE compat_access_tokens SET expires=0 WHERE hash=?", digest(access))
	if w = accessRequest(s, "POST", "/api/open/message/list", `{}`, access); w.Code != 401 {
		t.Fatal("expired key accepted")
	}
	if w = do(s, "POST", "/api/v1/manage/keys/access", encode(params{"id": k["id"], "enabled": false}), session); w.Code != 200 {
		t.Fatal("disable failed")
	}
	if w = accessRequest(s, "POST", "/api/open/message/list", `{}`, newAccess); w.Code != 401 {
		t.Fatal("disabled settings accepted")
	}
}

func TestWxPusherDeletionAndRetiredQuery(t *testing.T) {
	s, u, _ := testServer(t)
	key := issued(t, s, u, "wxpusher", Targets{Devices: []string{"DEV_a"}})
	other := issued(t, s, u, "wxpusher", Targets{Devices: []string{"DEV_b"}})
	w := do(s, "POST", "/api/send/message", encode(params{"appToken": key, "content": "<b>正文</b><script>alert(1)</script>", "contentType": 2, "uids": []string{u.ID}}), "")
	var result struct {
		ID   string `json:"message_id"`
		Data []struct {
			ID int64 `json:"messageContentId"`
		}
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil || len(result.Data) != 1 {
		t.Fatalf("send %s", w.Body)
	}
	path := "/api/send/message?appToken=" + other + "&messageContentId=" + encode(result.Data[0].ID)
	if w = do(s, "DELETE", path, "", ""); w.Code != 404 {
		t.Fatalf("cross-key content delete %d", w.Code)
	}
	if w = do(s, "GET", "/wxpusher/message/"+result.ID, "", ""); w.Code != 200 || strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("unsafe landing content")
	}
	path = strings.Replace(path, other, key, 1)
	for i := 0; i < 2; i++ {
		if w = do(s, "DELETE", path, "", ""); w.Code != 200 {
			t.Fatalf("idempotent delete %s", w.Body)
		}
	}
	if w = do(s, "GET", "/wxpusher/message/"+result.ID, "", ""); w.Code != 410 {
		t.Fatal("deleted content readable")
	}
	for _, path := range []string{"/api/send/query/status", "/api/send/query/status?sendRecordId=bad", "/api/send/query/null", "/api/send/query/undefined"} {
		w = do(s, "GET", path, "", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":1001`) || !strings.Contains(w.Body.String(), "停用") {
			t.Fatalf("retired query %s", w.Body)
		}
	}
}

func TestAccessSafeIPDoesNotTrustForwarding(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.RemoteAddr = "192.0.2.4:1234"
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	if safePeer(r, []string{"127.0.0.1"}) || !safePeer(r, []string{"192.0.2.0/24"}) {
		t.Fatal("IP allowlist bypassed or rejects valid CIDR")
	}
}

func TestWxPusherQRRequiresOwnerConfirmation(t *testing.T) {
	s, u, v := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	_, other, _ := s.store.login(v.Name, "bob-test-password")
	result, e := s.store.Exec("INSERT INTO topics(user_id,name) VALUES(?,?)", u.ID, "subscription-test")
	if e != nil {
		t.Fatal(e)
	}
	topic, _ := result.LastInsertId()
	key := issued(t, s, u, "wxpusher", Targets{Topics: []int64{topic}})
	w := do(s, "POST", "/api/fun/create/qrcode", encode(params{"appToken": key, "extra": "业务参数🔔", "validTime": 1800}), "")
	var p struct {
		Data struct{ Code, URL, Link string }
	}
	if e = json.Unmarshal(w.Body.Bytes(), &p); e != nil || w.Code != 200 || p.Data.Code == "" {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	code := p.Data.Code
	w = do(s, "GET", p.Data.URL, "", "")
	if w.Code != 200 {
		t.Fatalf("image %s", w.Body)
	}
	if _, e = png.Decode(bytes.NewReader(w.Body.Bytes())); e != nil {
		t.Fatal(e)
	}
	claim := encode(params{"code": code, "device_id": "DEV_a", "topic_ids": []int64{topic}})
	if w = do(s, "POST", "/api/v1/manage/subscriptions/claim", claim, ""); w.Code != 401 {
		t.Fatal("scan silently authorizes")
	}
	if w = do(s, "POST", "/api/v1/manage/subscriptions/claim", claim, other); w.Code != 403 {
		t.Fatal("cross-user subscribe")
	}
	if w = do(s, "POST", "/api/v1/manage/subscriptions/claim", encode(params{"code": code, "device_id": "DEV_c", "topic_ids": []int64{topic}}), session); w.Code != 403 {
		t.Fatal("foreign device bound")
	}
	if w = do(s, "POST", "/api/v1/manage/subscriptions/claim", encode(params{"code": code, "device_id": "DEV_a", "topic_ids": []int64{topic + 1}}), session); w.Code != 403 {
		t.Fatal("out of scope topic bound")
	}
	if w = do(s, "POST", "/api/v1/manage/subscriptions/preview", encode(params{"code": code}), session); w.Code != 200 || !strings.Contains(w.Body.String(), "业务参数") {
		t.Fatalf("preview %s", w.Body)
	}
	if w = do(s, "GET", "/api/fun/scan-qrcode-uid?code="+code, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"data":""`) {
		t.Fatal("unconfirmed UID returned")
	}
	for i := 0; i < 2; i++ {
		if w = do(s, "POST", "/api/v1/manage/subscriptions/claim", claim, session); w.Code != 200 {
			t.Fatalf("confirm %s", w.Body)
		}
	}
	if w = do(s, "GET", "/api/fun/scan-qrcode-uid?code="+code, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "DEV_a") {
		t.Fatalf("scan query %s", w.Body)
	}
	if w = do(s, "POST", "/api/send/message", encode(params{"appToken": key, "content": "订阅后推送", "uids": []string{"DEV_a"}}), ""); w.Code != 200 {
		t.Fatalf("subscribed device cannot receive %s", w.Body)
	}
	s.store.Exec("UPDATE wx_qr SET expires=0 WHERE hash=?", digest(code))
	if w = do(s, "POST", "/api/v1/manage/subscriptions/claim", claim, session); w.Code != 404 {
		t.Fatal("expired invitation accepted")
	}
	if w = do(s, "GET", p.Data.URL, "", ""); w.Code != 404 {
		t.Fatal("expired QR served")
	}
}

func TestWxPusherDesktopSocketFramesAndReceiptBoundary(t *testing.T) {
	s, u, _ := testServer(t)
	server := httptest.NewServer(s)
	defer server.Close()
	socket, e := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws?pushToken=DEV_a", "", server.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer socket.Close()
	socket.SetDeadline(time.Now().Add(8 * time.Second))
	var p params
	if e = websocket.JSON.Receive(socket, &p); e != nil || p.num("msgType") != 202 || p.str("pushToken") != "DEV_a" {
		t.Fatalf("init %v %v", p, e)
	}
	if e = websocket.JSON.Send(socket, params{"msgType": 101, "createTime": time.Now().UnixMilli()}); e != nil {
		t.Fatal(e)
	}
	if e = websocket.JSON.Receive(socket, &p); e != nil || p.num("msgType") != 201 {
		t.Fatalf("heart %v %v", p, e)
	}
	k := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	if w := do(s, "POST", "/api/v1/device/profile", `{"mode":"direct","app_version":"wx-wire-test"}`, "DEV_a"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	w := do(s, "POST", "/api/v1/push", `{"body":"实际WS消息","title":"中文标题"}`, k)
	var accepted params
	json.Unmarshal(w.Body.Bytes(), &accepted)
	var m Message
	var payload string
	s.store.QueryRow("SELECT payload FROM messages WHERE id=?", accepted.str("message_id")).Scan(&payload)
	json.Unmarshal([]byte(payload), &m)
	if _, e = s.dispatchDevice(context.Background(), "DEV_a", m, "inbox_DEV_a"); e != nil {
		t.Fatal(e)
	}
	if e = websocket.JSON.Receive(socket, &p); e != nil || p.num("msgType") != 20001 || p.str("content") != "实际WS消息" || int64(p.num("mid")) != m.ContentID {
		t.Fatalf("push %v %v", p, e)
	}
	var count int
	s.store.QueryRow("SELECT count(*) FROM receipts WHERE message_id=?", m.ID).Scan(&count)
	if count != 0 {
		t.Fatal("socket write forged an APP receipt")
	}
	if w = do(s, "POST", "/api/v1/device/receipts", encode(params{"receipts": []params{{"message_id": m.ID, "stage": "received"}}}), "DEV_a"); w.Code != 200 {
		t.Fatalf("explicit receipt %s", w.Body)
	}
}
