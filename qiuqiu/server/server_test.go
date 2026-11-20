package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, User, User) {
	t.Helper()
	s, e := newServer(Config{Database: ":memory:", AdminName: "admin", AdminPassword: "valid-test-password"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.store.Close() })
	u, e := s.store.addUser("alice", "alice-test-password", "user")
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.store.addUser("bob", "bob-test-password", "user")
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range []struct{ id, uid string }{{"DEV_a", u.ID}, {"DEV_b", u.ID}, {"DEV_c", v.ID}} {
		_, e = s.store.Exec("INSERT INTO devices VALUES(?,?,?,?,?,?,0)", d.id, d.uid, d.id, "inbox_"+d.id, digest(d.id), "ntfy_"+d.id)
		if e != nil {
			t.Fatal(e)
		}
	}
	return s, u, v
}
func do(s *Server, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func issued(t *testing.T, s *Server, u User, kind string, targets Targets) string {
	t.Helper()
	k, e := s.store.issueKey(u.ID, "", kind, "test", targets)
	if e != nil {
		t.Fatal(e)
	}
	return k["token"].(string)
}
func TestProtocolMatrix(t *testing.T) {
	s, u, _ := testServer(t)
	target := Targets{Devices: []string{"DEV_a"}}
	cases := []struct {
		kind, path string
		p          params
	}{
		{"native", "/api/v1/push", params{"title": "中文通知", "body": "正文 + / ? 🌸", "click": "https://example.com/path?a=1"}},
		{"serverchan", "/sctapi/{key}.send", params{"text": "标题别名", "desp": "**中文** 正文"}},
		{"sc3", "/sc3/" + sc3UID(u.ID) + "/send/{key}.send", params{"title": "标题", "desp": "消息", "tags": "server|urgent"}},
		{"pushplus", "/send/{key}", params{"title": "任务", "content": "<b>完成</b>", "template": "html"}},
		{"bark", "/push", params{"title": "标题", "body": "正文", "url": "https://example.com"}},
		{"wxpusher", "/api/send/message", params{"summary": "摘要", "content": "正文", "uids": []string{u.ID}, "contentType": 3}},
		{"serverchan", "/pushbear/sub", params{"text": "旧接口", "desp": "消息"}},
	}
	for _, tc := range cases {
		token := issued(t, s, u, tc.kind, target)
		tc.p[map[string]string{"native": "token", "serverchan": "sendkey", "sc3": "sendkey", "pushplus": "token", "bark": "device_key", "wxpusher": "appToken"}[tc.kind]] = token
		path := strings.ReplaceAll(tc.path, "{key}", token)
		for _, mode := range []string{"get", "json", "form"} {
			t.Run(tc.kind+"_"+mode, func(t *testing.T) {
				query := url.Values{}
				for k, v := range tc.p {
					switch v := v.(type) {
					case string:
						query.Set(k, v)
					default:
						query.Set(k, encode(v))
					}
				}
				var r *http.Request
				switch mode {
				case "get":
					r = httptest.NewRequest("GET", path+"?"+query.Encode(), nil)
				case "json":
					r = httptest.NewRequest("POST", path, strings.NewReader(encode(tc.p)))
					r.Header.Set("Content-Type", "application/json")
				default:
					r = httptest.NewRequest("POST", path, strings.NewReader(query.Encode()))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code >= 300 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				var result map[string]any
				if json.Unmarshal(w.Body.Bytes(), &result) != nil {
					t.Fatal("invalid response")
				}
			})
		}
	}
}
func TestIsolationScopeAndFanout(t *testing.T) {
	s, u, v := testServer(t)
	res, e := s.store.Exec("INSERT INTO topics(user_id,name) VALUES(?,?)", u.ID, "servers")
	if e != nil {
		t.Fatal(e)
	}
	topic, _ := res.LastInsertId()
	_, _ = s.store.Exec("INSERT INTO topic_devices VALUES(?,?),(?,?)", topic, "DEV_a", topic, "DEV_b")
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}, Topics: []int64{topic}})
	w := do(s, "POST", "/api/v1/push", `{"topic":"servers","device_ids":["DEV_a"],"body":"hello"}`, token)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var n int
	s.store.QueryRow("SELECT count(*) FROM deliveries").Scan(&n)
	if n != 2 {
		t.Fatal("fanout not deduplicated", n)
	}
	for _, payload := range []string{`{"device_ids":["DEV_c"],"body":"secret"}`, `{"topic":"unknown","body":"secret"}`} {
		w = do(s, "POST", "/api/v1/push", payload, token)
		if w.Code != 403 {
			t.Fatal("cross-user target accepted", w.Code, w.Body.String())
		}
	}
	if _, e = s.store.issueKey(u.ID, "", "native", "bad", Targets{Devices: []string{"DEV_c"}}); e == nil {
		t.Fatal("cross-user key issued")
	}
	w = do(s, "POST", "/api/v1/push", `{"body":"secret"}`, issued(t, s, v, "bark", Targets{Devices: []string{"DEV_c"}}))
	if w.Code != 401 {
		t.Fatal("cross-protocol key accepted")
	}
	_, _ = s.store.Exec("UPDATE api_keys SET disabled=1 WHERE hash=?", digest(token))
	if w = do(s, "POST", "/api/v1/push", `{"body":"secret"}`, token); w.Code != 401 {
		t.Fatal("revoked key accepted")
	}
}
func TestIdempotency(t *testing.T) {
	s, u, _ := testServer(t)
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	ids := []string{}
	for _, body := range []string{"first", "first", "changed"} {
		r := httptest.NewRequest("POST", "/api/v1/push", strings.NewReader(encode(params{"body": body})))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "fixed")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if body == "changed" {
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
		} else {
			if w.Code != 202 {
				t.Fatal(w.Code, w.Body.String())
			}
			var p params
			json.Unmarshal(w.Body.Bytes(), &p)
			ids = append(ids, p.str("message_id"))
		}
	}
	if ids[0] != ids[1] {
		t.Fatal("duplicate created")
	}
	var count int
	s.store.QueryRow("SELECT count(*) FROM deliveries").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
}
func TestValidationAndEncoding(t *testing.T) {
	cases := []struct {
		path, body string
		status     int
	}{
		{"/push", `{"device_key":"bad","body":"正文","icon":"https://evil/icon"}`, 400},
		{"/api/v1/push", `{"body":"正文","click":"javascript:alert(1)"}`, 400},
		{"/api/v1/push", `{"body":""}`, 400},
		{"/api/v1/push", `{"body":"hello","priority":9}`, 400},
		{"/api/v1/push", encode(params{"body": strings.Repeat("中", 30000)}), 400},
		{"/api/v1/push", `{"body":"hello"} {}`, 400},
		{"/sc3/999/send/sctp123tx.send", `{"desp":"hello"}`, 400},
	}
	s, _, _ := testServer(t)
	for _, tc := range cases {
		w := do(s, "POST", tc.path, tc.body, "")
		if w.Code != tc.status {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("GET", "/bark/key/%E4%B8%AD%E6%96%87/a%2Fb%2B%25", nil)
	m, _, _, e := adapt(r)
	if e != nil || m.Body != "a/b+%" || m.Title != "中文" {
		t.Fatal(m, e)
	}
}
func TestWorkerRetryAndPersistence(t *testing.T) {
	s, u, _ := testServer(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(503)
			return
		}
		var p params
		json.NewDecoder(r.Body).Decode(&p)
		if p["qiuqiu"] == nil || p.str("sequence_id") == "" {
			t.Error("metadata missing")
		}
		respond(w, 200, params{"id": "ntfy123"})
	}))
	defer upstream.Close()
	s.cfg.NtfyURL = upstream.URL
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	w := do(s, "POST", "/api/v1/push", `{"body":"test"}`, token)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	for i := 0; i < 3; i++ {
		if e := s.deliverPending(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	var state, ntfyID string
	s.store.QueryRow("SELECT status,ntfy_id FROM deliveries").Scan(&state, &ntfyID)
	if state != "published" || ntfyID != "ntfy123" {
		t.Fatal(state, ntfyID)
	}
	if e := s.deliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 3 {
		t.Fatal("duplicate publish")
	}
}
func TestManagementAuthorizationAndPairExpiry(t *testing.T) {
	s, u, v := testServer(t)
	_, token, e := s.store.login(u.Name, "alice-test-password")
	if e != nil {
		t.Fatal(e)
	}
	if w := do(s, "GET", "/api/v1/manage/users", "", token); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := do(s, "GET", "/api/v1/manage/keys", "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	_, _ = s.store.Exec("INSERT INTO pairings VALUES(?,?,?,0)", digest("expired"), v.ID, time.Now().Add(-time.Second).Unix())
	if w := do(s, "POST", "/api/v1/pair/claim", `{"code":"expired","name":"phone"}`, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := do(s, "POST", "/api/v1/logout", "", token); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := do(s, "GET", "/api/v1/me", "", token); w.Code != 401 {
		t.Fatal("logout ineffective")
	}
}
func TestHTMLAndCallbackSafety(t *testing.T) {
	s, u, _ := testServer(t)
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	w := do(s, "POST", "/api/v1/push", `{"body":"<script>alert(1)</script><b>hello</b>","content_type":"html"}`, token)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var payload string
	s.store.QueryRow("SELECT payload FROM messages").Scan(&payload)
	if strings.Contains(payload, "script") {
		t.Fatal("unsanitized HTML")
	}
	for _, raw := range []string{"http://example.com", "https://127.0.0.1/", "https://example.com/unlisted"} {
		if s.checkCallback(raw) == nil {
			t.Fatal("unapproved callback")
		}
	}
}

func TestServerchanTitleOnly(t *testing.T) {
	m, _, _, e := adapt(httptest.NewRequest("GET", "/sctapi/key.send?title=hello", nil))
	if e != nil || m.Title != "hello" || m.Body != "hello" {
		t.Fatal(m, e)
	}
}
func TestStatsCountRetriesNotOnlyMessages(t *testing.T) {
	s, u, _ := testServer(t)
	token := issued(t, s, u, "native", Targets{Devices: []string{"DEV_a"}})
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/api/v1/push", strings.NewReader(`{"body":"same"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "same-key")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var calls, messages int
	s.store.QueryRow("SELECT sum(calls) FROM api_usage").Scan(&calls)
	s.store.QueryRow("SELECT count(*) FROM messages").Scan(&messages)
	if calls != 2 || messages != 1 {
		t.Fatal(calls, messages)
	}
}
