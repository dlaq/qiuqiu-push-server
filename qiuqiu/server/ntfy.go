package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/microcosm-cc/bluemonday"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

var htmlPolicy = bluemonday.UGCPolicy()

func (s *Server) ntfyRequest(ctx context.Context, method, path string, body any, out any, admin bool) error {
	b := []byte(encode(body))
	u := s.cfg.NtfyURL + path
	client := s.client
	if admin {
		client = s.adminClient
		if s.cfg.NtfySocket != "" {
			u = "http://localhost" + path
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	if !admin || s.cfg.NtfySocket == "" {
		req.SetBasicAuth(s.cfg.NtfyAdminName, s.cfg.NtfyAdminPassword)
	}
	resp, e := client.Do(req)
	if e != nil {
		return fmt.Errorf("ntfy unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ntfy status %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return nil
}
func (s *Server) provisionDevice(ctx context.Context, uid, name string) (map[string]any, error) {
	s.provision.Lock()
	defer s.provision.Unlock()
	id := "DEV_" + strings.ReplaceAll(uuid(), "-", "")
	inbox := "qq_" + strings.ReplaceAll(uuid(), "-", "")
	username := "d_" + strings.TrimPrefix(id, "DEV_")
	password := secret("")
	if e := s.ntfyRequest(ctx, "POST", "/v1/users", map[string]string{"username": username, "password": password}, nil, true); e != nil {
		return nil, e
	}
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.ntfyRequest(c, "DELETE", "/v1/users", map[string]string{"username": username}, nil, true)
	}
	if e := s.ntfyRequest(ctx, "PUT", "/v1/users/access", map[string]string{"username": username, "topic": inbox, "permission": "read-only"}, nil, true); e != nil {
		cleanup()
		return nil, e
	}
	var token struct {
		Token string `json:"token"`
	}
	if e := s.ntfyRequest(ctx, "POST", "/v1/users/tokens", map[string]string{"username": username, "label": "QiuQiu device"}, &token, true); e != nil {
		cleanup()
		return nil, e
	}
	if token.Token == "" {
		cleanup()
		return nil, fmt.Errorf("ntfy empty token")
	}
	deviceToken := secret("qd_")
	_, e := s.store.Exec("INSERT INTO devices(id,user_id,name,inbox,token_hash,ntfy_user) VALUES(?,?,?,?,?,?)", id, uid, name, inbox, digest(deviceToken), username)
	if e != nil {
		cleanup()
		return nil, e
	}
	return map[string]any{"device_id": id, "user_id": uid, "device_token": deviceToken, "ntfy_token": token.Token, "ntfy_username": username, "topic": inbox, "ntfy_url": s.cfg.NtfyPublicURL}, nil
}
func (s *Server) publish(ctx context.Context, m Message, topic string) (string, error) {
	meta := map[string]any{"message_id": m.ID, "business_type": m.Business, "icon": m.Icon, "sound": m.Sound, "source_protocol": m.Protocol, "content_type": m.ContentType, "group": m.Group}
	body := map[string]any{"topic": topic, "title": m.Title, "message": m.Body, "priority": m.Priority, "click": m.Click, "tags": m.Tags, "markdown": m.ContentType == "markdown", "sequence_id": first(m.Sequence, m.ID), "qiuqiu": meta}
	if m.Ntfy != nil {
		for _, field := range []string{"actions", "icon", "encoding"} {
			if v, ok := m.Ntfy[field]; ok {
				body[field] = v
			}
		}
		if attachment, ok := m.Ntfy["attachment"].(map[string]any); ok {
			body["attach"] = attachment["url"]
			body["filename"] = attachment["name"]
		}
	}
	var response struct {
		ID string `json:"id"`
	}
	if e := s.ntfyRequest(ctx, "POST", "/", body, &response, false); e != nil {
		return "", e
	}
	if response.ID == "" {
		return "", fmt.Errorf("ntfy did not return a message id")
	}
	return response.ID, nil
}
func (s *Server) runWorker(ctx context.Context) {
	// Recover a process interrupted between submitting a message and recording the response.
	_, _ = s.store.Exec("UPDATE deliveries SET status=CASE WHEN attempts>=3 THEN 'failed' ELSE 'pending' END WHERE status='sending'")
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if e := s.deliverPending(ctx); e != nil {
			s.store.audit("", "worker_error", "投递队列处理失败")
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}
func (s *Server) deliverPending(ctx context.Context) error {
	if _, err := s.store.Exec("UPDATE messages SET status=CASE WHEN EXISTS(SELECT 1 FROM deliveries WHERE message_id=messages.id AND status='failed') THEN 'failed' ELSE 'published' END WHERE status='accepted' AND EXISTS(SELECT 1 FROM deliveries WHERE message_id=messages.id) AND NOT EXISTS(SELECT 1 FROM deliveries WHERE message_id=messages.id AND status IN ('pending','sending'))"); err != nil {
		return err
	}
	rows, e := s.store.list("SELECT d.id,d.message_id,d.topic,d.attempts,m.payload FROM deliveries d JOIN messages m ON m.id=d.message_id WHERE d.status='pending' AND d.attempts<3 ORDER BY d.id LIMIT 50")
	if e != nil {
		return e
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		id := row["id"]
		var m Message
		if e = json.Unmarshal([]byte(row["payload"].(string)), &m); e != nil {
			return e
		}
		var disabled int
		e = s.store.QueryRow("SELECT count(*) FROM deliveries d JOIN devices v ON d.device_id=v.id JOIN users u ON u.id=v.user_id WHERE d.id=? AND v.disabled=0 AND u.disabled=0", id).Scan(&disabled)
		if e != nil {
			return e
		}
		if disabled == 0 || m.Expires > 0 && m.Expires <= time.Now().Unix() {
			_, e = s.store.Exec("UPDATE deliveries SET status='failed',error='目标已禁用或消息已过期' WHERE id=?", id)
			if e != nil {
				return e
			}
		} else {
			if _, e = s.store.Exec("UPDATE deliveries SET status='sending',attempts=attempts+1 WHERE id=?", id); e != nil {
				return e
			}
			ntfyID, sendErr := s.publish(ctx, m, row["topic"].(string))
			state, errText := "published", ""
			if sendErr != nil {
				state = "pending"
				if row["attempts"].(int64) >= 2 {
					state = "failed"
				}
				errText = "ntfy 投递失败"
			}
			if _, e = s.store.Exec("UPDATE deliveries SET status=?,ntfy_id=?,error=? WHERE id=?", state, ntfyID, errText, id); e != nil {
				return e
			}
		}
		var pending, failed int
		if e = s.store.QueryRow("SELECT count(*) FROM deliveries WHERE message_id=? AND status IN ('pending','sending')", m.ID).Scan(&pending); e != nil {
			return e
		}
		if pending == 0 {
			if e = s.store.QueryRow("SELECT count(*) FROM deliveries WHERE message_id=? AND status='failed'", m.ID).Scan(&failed); e != nil {
				return e
			}
			state := "published"
			if failed > 0 {
				state = "failed"
			}
			if _, e = s.store.Exec("UPDATE messages SET status=? WHERE id=?", state, m.ID); e != nil {
				return e
			}
			s.sendCallback(ctx, m, state)
		}
	}
	return nil
}
func (s *Server) checkCallback(raw string) error {
	if raw == "" {
		return nil
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return bad("callbackUrl 必须是允许的 HTTPS URL")
	}
	for _, v := range strings.Split(s.cfg.AllowedCallbacks, ",") {
		if raw == strings.TrimSpace(v) {
			return nil
		}
	}
	return bad("callbackUrl 不在管理员允许列表")
}
func (s *Server) sendCallback(ctx context.Context, m Message, state string) {
	if m.Callback == "" {
		return
	}
	if s.checkCallback(m.Callback) != nil {
		return
	}
	// Restrict resolved addresses too, including after DNS changes; redirects cannot bypass the allowlist.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, host)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
				return nil, fmt.Errorf("private callback address")
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("empty address")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}}
	req, e := http.NewRequestWithContext(ctx, "POST", m.Callback, strings.NewReader(encode(map[string]string{"messageId": m.ID, "status": state, "meaning": "server publication result"})))
	if e != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		s.store.audit(m.UserID, "callback_failed", m.ID)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.store.audit(m.UserID, "callback_failed", m.ID)
	}
}
func (s *Server) nativeProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" || r.Method == "PUT" {
		if key, err := s.store.key(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "ntfy"); err == nil {
			s.store.usage(key)
		}
	}
	target, e := url.Parse(s.cfg.NtfyURL)
	if e != nil {
		http.Error(w, "ntfy unavailable", 502)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "ntfy unavailable", 502) }
	proxy.ModifyResponse = func(resp *http.Response) error {
		if (r.Method == "POST" || r.Method == "PUT") && resp.StatusCode == 200 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if e != nil {
				return e
			}
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(b))
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && m["event"] == "message" {
				if err := s.recordNative(r, b); err != nil {
					s.store.audit("", "native_record_failed", "原生消息记录或群发失败")
					return err
				}
			}
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}
