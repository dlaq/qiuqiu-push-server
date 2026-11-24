package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderRequestsAndRefusal(t *testing.T) {
	s, _, _ := testServer(t)
	for _, kind := range []string{"wecom_bot", "feishu_bot", "dingtalk", "telegram", "discord", "bark", "onebot", "wecom_app", "wechat_test", "feishu_app", "qq_bot", "webhook"} {
		t.Run(kind, func(t *testing.T) {
			sends := 0
			s.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				response := `{"errcode":0,"code":0,"retcode":0,"status":"ok","ok":true,"id":"provider-id"}`
				if kind == "bark" {
					response = `{"code":200,"message":"success"}`
				}
				if strings.Contains(r.URL.Path, "gettoken") || strings.HasSuffix(r.URL.Path, "/token") || strings.Contains(r.URL.Path, "getAppAccessToken") || strings.Contains(r.URL.Path, "tenant_access_token") {
					response = `{"access_token":"test-token","tenant_access_token":"test-token","code":0,"errcode":0}`
				} else {
					sends++
					var payload params
					if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
						t.Fatal(e)
					}
					if kind == "onebot" && payload.str("user_id") != "123" {
						t.Fatalf("OneBot wrong recipient: %v", payload)
					}
					if kind == "telegram" && payload.str("chat_id") != "123" {
						t.Fatal("Telegram lost recipient")
					}
					if !strings.Contains(encode(payload), "中文正文") && !strings.Contains(encode(payload), "\\u4e2d") {
						t.Fatalf("body lost: %v", payload)
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			c := ChannelConfig{"url": "https://example.com/send", "token": "test", "key": "test", "app_id": "app", "secret": "test", "agent_id": "1", "recipient": "123", "template_id": "template"}
			if e := s.sendExternal(context.Background(), kind, c, Message{ID: "test-id", Title: "标题", Body: "中文正文"}); e != nil {
				t.Fatal(e)
			}
			if sends != 1 {
				t.Fatalf("expected one send, got %d", sends)
			}
		})
	}
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":40001}`))}, nil
	})
	if s.sendExternal(context.Background(), "wecom_bot", ChannelConfig{"url": "https://example.com"}, Message{Body: "test"}) == nil {
		t.Fatal("false provider success")
	}
	for _, tc := range []struct{ kind, body string }{
		{"wecom_bot", `{"errcode":"not-a-number"}`}, {"wecom_bot", `{"errcode":null}`}, {"wecom_bot", `{"errcode":false}`}, {"wecom_bot", `{"errcode":{}}`},
		{"wecom_bot", `{"errcode":1e-999}`}, {"wecom_bot", `{"errcode":0} {"errcode":40001}`},
		{"feishu_bot", `{"code":0.5}`}, {"feishu_bot", `{}`}, {"bark", `{"code":0}`}, {"onebot", `{"retcode":0,"status":"failed"}`},
	} {
		s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		c := ChannelConfig{"url": "https://example.com", "recipient": "123"}
		if s.sendExternal(context.Background(), tc.kind, c, Message{Body: "test"}) == nil {
			t.Fatalf("malformed %s response accepted: %s", tc.kind, tc.body)
		}
	}
}

func TestBarkEncryptionAndIconUpload(t *testing.T) {
	s, u, _ := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	k, e := s.store.issueKey(u.ID, "", "bark", "cipher", Targets{Devices: []string{"DEV_a"}})
	if e != nil {
		t.Fatal(e)
	}
	key := []byte("1234567890123456")
	iv := []byte("6543210987654321")
	w := do(s, "POST", "/api/v1/manage/keys/encryption", encode(params{"id": k["id"], "algorithm": "cbc", "key": string(key), "iv": string(iv)}), session)
	if w.Code != 200 {
		t.Fatalf("config %d %s", w.Code, w.Body)
	}
	plain := []byte(`{"body":"加密中文","title":"测试"}`)
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, _ := aes.NewCipher(key)
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, plain)
	w = do(s, "POST", "/bark/"+k["token"].(string), encode(params{"ciphertext": base64.StdEncoding.EncodeToString(encrypted)}), "")
	if w.Code != 200 {
		t.Fatalf("encrypted send: %d %s", w.Code, w.Body)
	}
	var img bytes.Buffer
	png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, _ := writer.CreateFormFile("file", "icon.png")
	file.Write(img.Bytes())
	writer.Close()
	req := httptest.NewRequest("POST", "/api/v1/manage/icons", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+session)
	result := httptest.NewRecorder()
	s.ServeHTTP(result, req)
	if result.Code != 201 {
		t.Fatalf("upload %d %s", result.Code, result.Body)
	}
	var response params
	json.Unmarshal(result.Body.Bytes(), &response)
	icon := do(s, "GET", response.str("path"), "", "")
	if icon.Code != 200 || icon.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("icon %d", icon.Code)
	}
}

func TestMigrationKeepsOldMessagesAndSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(schema); e != nil {
		t.Fatal(e)
	}
	old := &Store{db}
	u, e := old.addUser("old-user", "test-admin-password", "admin")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec("INSERT INTO messages(id,user_id,protocol,payload,status,created_at) VALUES('old',?,'native','{}','published',1)", u.ID)
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	s, e := newServer(Config{Database: path})
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := s.sealConfig(ChannelConfig{"token": "do-not-lose"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.store.Exec("INSERT INTO outbound_channels VALUES('saved',?,'saved','webhook',?,0)", u.ID, sealed); e != nil {
		t.Fatal(e)
	}
	key := append([]byte{}, s.channelKey...)
	s.store.Close()
	s, e = newServer(Config{Database: path})
	if e != nil {
		t.Fatal(e)
	}
	defer s.store.Close()
	if !bytes.Equal(key, s.channelKey) {
		t.Fatal("encryption key changed")
	}
	var n int
	s.store.QueryRow("SELECT count(*) FROM messages WHERE id='old'").Scan(&n)
	if n != 1 {
		t.Fatal("historical message lost")
	}
	_, config, e := s.readChannel(u.ID, "saved")
	if e != nil || config["token"] != "do-not-lose" {
		t.Fatalf("saved secret cannot be decrypted after restart: %v", e)
	}
}

func TestChannelEditingAndSenderStatusScope(t *testing.T) {
	s, u, v := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	_, otherSession, _ := s.store.login(v.Name, "bob-test-password")
	w := do(s, "POST", "/api/v1/manage/channels", encode(params{"name": "hook", "kind": "webhook", "config": params{"url": "https://example.com/hook", "token": "old-secret"}}), session)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	var created params
	json.Unmarshal(w.Body.Bytes(), &created)
	id := created.str("id")
	body := encode(params{"id": id, "name": "updated", "kind": "webhook", "config": params{"url": "https://example.com/new", "token": "new-secret"}})
	if w = do(s, "PATCH", "/api/v1/manage/channels", body, otherSession); w.Code < 400 {
		t.Fatal("cross-user channel update")
	}
	if w = do(s, "PATCH", "/api/v1/manage/channels", body, session); w.Code != 200 {
		t.Fatalf("update %d %s", w.Code, w.Body)
	}
	_, c, e := s.readChannel(u.ID, id)
	if e != nil || c["token"] != "new-secret" {
		t.Fatal("replacement lost")
	}
	if w = do(s, "POST", "/api/v1/manage/channels", encode(params{"name": "invalid", "kind": "webhook"}), session); w.Code != 400 {
		t.Fatal("accepted incomplete channel")
	}
	key := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	otherKey := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	w = do(s, "POST", "/api/v1/push", `{"body":"scoped status"}`, key)
	if w.Code != 202 {
		t.Fatalf("send %d %s", w.Code, w.Body)
	}
	var sent params
	json.Unmarshal(w.Body.Bytes(), &sent)
	path := "/api/v1/messages/" + sent.str("message_id")
	if w = do(s, "GET", path, "", key); w.Code != 200 || !strings.Contains(w.Body.String(), "scoped status") {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if w = do(s, "GET", path, "", otherKey); w.Code != 404 {
		t.Fatal("sibling key read another application's message")
	}
}

func TestBarkAndSimplePushBatch(t *testing.T) {
	s, u, _ := testServer(t)
	for _, kind := range []string{"bark", "wxpusher_spt"} {
		key := issued(t, s, u, kind, Targets{Devices: []string{"DEV_a"}})
		path, body := "/push", params{"device_keys": []string{key, key, "revoked"}, "body": "批量正文"}
		if kind == "wxpusher_spt" {
			path = "/api/send/message/simple-push"
			body = params{"sptList": []string{key, key, "revoked"}, "content": "批量正文"}
		}
		w := do(s, "POST", path, encode(body), "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "duplicate") || !strings.Contains(w.Body.String(), "unauthorized") {
			t.Fatalf("batch %s: %d %s", kind, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), key) {
			t.Fatal("batch echoed secret")
		}
		var count int
		s.store.QueryRow("SELECT count(*) FROM messages WHERE key_id=(SELECT id FROM api_keys WHERE hash=?)", digest(key)).Scan(&count)
		if count != 1 {
			t.Fatalf("duplicate key created %d messages", count)
		}
	}
}

func TestOfflineDirectQueueDoesNotBlockFCM(t *testing.T) {
	s, u, _ := testServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respond(w, 200, params{"id": "upstream-id"})
	}))
	defer upstream.Close()
	s.cfg.NtfyURL = upstream.URL
	s.store.Exec("INSERT INTO device_profiles(device_id,mode) VALUES('DEV_a','direct')")
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a", "DEV_b"}})
	for i := 0; i < 70; i++ {
		if w := do(s, "POST", "/api/v1/push", `{"body":"offline backlog","device_ids":["DEV_a"]}`, token); w.Code != 202 {
			t.Fatalf("backlog %d", w.Code)
		}
	}
	if w := do(s, "POST", "/api/v1/push", `{"body":"online FCM","device_ids":["DEV_b"]}`, token); w.Code != 202 {
		t.Fatalf("send %d", w.Code)
	}
	if e := s.deliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatalf("FCM blocked by direct backlog, calls=%d", calls.Load())
	}
	var count int
	s.store.QueryRow("SELECT count(*) FROM deliveries WHERE device_id='DEV_a' AND status='pending' AND attempts=0").Scan(&count)
	if count != 70 {
		t.Fatalf("offline retry budget consumed: %d", count)
	}
}

func TestNativeAdvancedNotificationValidation(t *testing.T) {
	s, u, _ := testServer(t)
	key := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	body := `{"body":"附件和操作","ntfy":{"attachment":{"url":"https://example.com/report.pdf","name":"报告.pdf"},"actions":[{"action":"view","label":"查看","url":"https://example.com"}]}}`
	w := do(s, "POST", "/api/v1/push", body, key)
	if w.Code != 202 {
		t.Fatalf("advanced %d %s", w.Code, w.Body)
	}
	var result params
	json.Unmarshal(w.Body.Bytes(), &result)
	trace := do(s, "GET", "/api/v1/messages/"+result.str("message_id"), "", key)
	if !strings.Contains(trace.Body.String(), "report.pdf") {
		t.Fatal("attachment lost")
	}
	w = do(s, "POST", "/api/v1/push", strings.ReplaceAll(body, "https://example.com", "javascript:alert(1)"), key)
	if w.Code != 400 {
		t.Fatalf("unsafe action/attachment accepted: %d", w.Code)
	}
}

func TestKeyDefaultRouteRoundTrip(t *testing.T) {
	s, u, _ := testServer(t)
	_, session, _ := s.store.login(u.Name, "alice-test-password")
	response := do(s, "POST", "/api/v1/manage/channels", `{"name":"仅存档","kind":"store"}`, session)
	var channel params
	json.Unmarshal(response.Body.Bytes(), &channel)
	if response.Code != 201 {
		t.Fatalf("channel %d %s", response.Code, response.Body)
	}
	response = do(s, "POST", "/api/v1/manage/policies", encode(params{"name": "存档策略", "mode": "single", "steps": []params{{"channel_id": channel.str("id")}}}), session)
	var policy params
	json.Unmarshal(response.Body.Bytes(), &policy)
	if response.Code != 201 {
		t.Fatalf("policy %d %s", response.Code, response.Body)
	}
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	key, _ := s.store.key(token, "native")
	response = do(s, "POST", "/api/v1/manage/keys/route", encode(params{"id": key.ID, "delivery_policy": policy.str("id")}), session)
	if response.Code != 200 {
		t.Fatalf("bind %d %s", response.Code, response.Body)
	}
	listed := do(s, "GET", "/api/v1/manage/keys", "", session)
	if !strings.Contains(listed.Body.String(), policy.str("id")) {
		t.Fatal("default policy not visible in credential list")
	}
	response = do(s, "POST", "/api/v1/push", `{"body":"走凭证默认策略"}`, token)
	if response.Code != 202 {
		t.Fatalf("send %d %s", response.Code, response.Body)
	}
	if err := s.deliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n int
	s.store.QueryRow("SELECT count(*) FROM route_attempts WHERE state='stored' AND channel_id=?", channel.str("id")).Scan(&n)
	if n != 1 {
		t.Fatal("stored policy was not used")
	}
}
