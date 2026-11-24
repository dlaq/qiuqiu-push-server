package main

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"html"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const compatibilitySchema = `
CREATE TABLE IF NOT EXISTS compat_access_settings(key_id TEXT PRIMARY KEY REFERENCES api_keys(id),secret_hash TEXT NOT NULL,safe_ips TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS compat_access_tokens(hash TEXT PRIMARY KEY,key_id TEXT NOT NULL REFERENCES api_keys(id),expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS compat_access_key ON compat_access_tokens(key_id);
CREATE TABLE IF NOT EXISTS compat_hidden_messages(key_id TEXT NOT NULL,message_id TEXT NOT NULL,PRIMARY KEY(key_id,message_id));
CREATE TABLE IF NOT EXISTS compat_deleted_contents(key_id TEXT NOT NULL,message_id TEXT NOT NULL,PRIMARY KEY(key_id,message_id));
CREATE TABLE IF NOT EXISTS wx_qr(hash TEXT PRIMARY KEY,key_id TEXT NOT NULL REFERENCES api_keys(id),extra TEXT NOT NULL,expires INTEGER NOT NULL,last_uid TEXT NOT NULL DEFAULT '');
INSERT OR IGNORE INTO schema_version VALUES(4);
`

// AccessKey is deliberately opt-in and remains scoped to its originating
// credential. It cannot read another application belonging to the same user.
func (s *Server) compatibilityManage(w http.ResponseWriter, r *http.Request, u User, resource string) (bool, error) {
	if resource == "subscriptions/claim" || resource == "subscriptions/preview" {
		return true, s.wxSubscribe(w, r, u, resource)
	}
	if resource != "keys/access" {
		return false, nil
	}
	if r.Method != "POST" {
		return true, &AppError{405, "method_not_allowed", "需要POST"}
	}
	var p struct {
		ID      string   `json:"id"`
		Secret  string   `json:"secret"`
		SafeIPs []string `json:"safe_ips"`
		Enabled bool     `json:"enabled"`
	}
	if e := decode(r, &p); e != nil {
		return true, e
	}
	var n int
	if e := s.store.QueryRow("SELECT count(*) FROM api_keys WHERE id=? AND user_id=? AND kind='pushplus' AND disabled=0", p.ID, u.ID).Scan(&n); e != nil {
		return true, e
	}
	if n != 1 {
		return true, forbidden()
	}
	if p.Enabled {
		if len(p.Secret) < 32 || len(p.Secret) > 256 || len(p.SafeIPs) > 32 {
			return true, bad("secretKey须为32–256字节，安全IP最多32条")
		}
		for _, ip := range p.SafeIPs {
			if _, e := netip.ParseAddr(ip); e != nil {
				if _, e = netip.ParsePrefix(ip); e != nil {
					return true, bad("安全IP须为IP地址或CIDR")
				}
			}
		}
	}
	tx, e := s.store.Begin()
	if e != nil {
		return true, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM compat_access_tokens WHERE key_id=?", p.ID); e != nil {
		return true, e
	}
	if p.Enabled {
		_, e = tx.Exec("INSERT INTO compat_access_settings VALUES(?,?,?) ON CONFLICT(key_id) DO UPDATE SET secret_hash=excluded.secret_hash,safe_ips=excluded.safe_ips", p.ID, digest(p.Secret), encode(p.SafeIPs))
	} else {
		_, e = tx.Exec("DELETE FROM compat_access_settings WHERE key_id=?", p.ID)
	}
	if e != nil {
		return true, e
	}
	if e = tx.Commit(); e != nil {
		return true, e
	}
	s.store.audit(u.ID, "pushplus_access_configured", "凭证 "+p.ID)
	respond(w, 200, params{"success": true, "enabled": p.Enabled})
	return true, nil
}

func compatibilityPath(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasPrefix(p, "/api/fun/") || strings.HasPrefix(p, "/api/open/message/") || p == "/api/common/openApi/getAccessKey" || strings.HasPrefix(p, "/shortMessage/") || strings.HasPrefix(p, "/wxpusher/message/") || strings.HasPrefix(p, "/api/send/query/") || (p == "/api/send/message" && r.Method == "DELETE")
}

func methodIs(r *http.Request, method string) error {
	if r.Method != method {
		return &AppError{405, "method_not_allowed", "需要" + method}
	}
	return nil
}

func safePeer(r *http.Request, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return false
	}
	peer, e := netip.ParseAddr(host)
	if e != nil {
		return false
	}
	peer = peer.Unmap()
	for _, v := range allowed {
		if address, e := netip.ParseAddr(v); e == nil && address.Unmap() == peer {
			return true
		}
		if prefix, e := netip.ParsePrefix(v); e == nil && prefix.Contains(peer) {
			return true
		}
	}
	return false
}

func (s *Server) compatibilityHTTP(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/fun/") {
		return s.wxQRHTTP(w, r)
	}
	if strings.HasPrefix(p, "/api/send/query/") {
		if e := methodIs(r, "GET"); e != nil {
			return e
		}
		respond(w, 200, params{"code": 1001, "msg": "发送状态查询已停用，请停止轮询；这不表示消息发送失败。球球真实状态请使用 /api/v1/messages/{UUID}", "success": false})
		return nil
	}
	if strings.HasPrefix(p, "/shortMessage/") || strings.HasPrefix(p, "/wxpusher/message/") {
		return s.compatibilityContent(w, r)
	}
	if p == "/api/send/message" {
		return s.wxDeleteContent(w, r)
	}
	if p == "/api/common/openApi/getAccessKey" {
		return s.pushPlusAccess(w, r)
	}
	key, e := s.pushPlusAccessKey(r)
	if e != nil {
		return e
	}
	switch p {
	case "/api/open/message/list":
		if e = methodIs(r, "POST"); e != nil {
			return e
		}
		var paging struct {
			Current  int `json:"current"`
			PageSize int `json:"pageSize"`
		}
		if e = decode(r, &paging); e != nil {
			return e
		}
		if paging.Current == 0 {
			paging.Current = 1
		}
		if paging.PageSize == 0 {
			paging.PageSize = 20
		}
		if paging.Current < 1 || paging.Current > 1000000 || paging.PageSize < 1 || paging.PageSize > 50 {
			return bad("分页参数无效，每页最多50条")
		}
		var total int
		filter := " FROM messages m WHERE m.key_id=? AND m.user_id=? AND m.protocol='pushplus' AND NOT EXISTS(SELECT 1 FROM compat_hidden_messages h WHERE h.key_id=m.key_id AND h.message_id=m.id)"
		if e = s.store.QueryRow("SELECT count(*)"+filter, key.ID, key.UserID).Scan(&total); e != nil {
			return e
		}
		rows, e := s.store.Query("SELECT m.payload"+filter+" ORDER BY m.created_at DESC,m.id DESC LIMIT ? OFFSET ?", key.ID, key.UserID, paging.PageSize, (paging.Current-1)*paging.PageSize)
		if e != nil {
			return e
		}
		list := []params{}
		for rows.Next() {
			var payload string
			if e = rows.Scan(&payload); e != nil {
				break
			}
			var m Message
			if e = json.Unmarshal([]byte(payload), &m); e != nil {
				break
			}
			kind := 1
			if m.Topic != "" || len(m.TopicIDs) > 0 {
				kind = 2
			}
			list = append(list, params{"shortCode": m.ID, "title": m.Title, "topicName": m.Topic, "messageType": kind, "channel": "qiuqiu", "updateTime": compatDate(m.CreatedAt)})
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return e
		}
		respond(w, 200, params{"code": 200, "msg": "请求成功", "data": params{"pageNum": paging.Current, "pageSize": paging.PageSize, "total": total, "pages": (total + paging.PageSize - 1) / paging.PageSize, "list": list}})
		return nil
	case "/api/open/message/sendMessageResult", "/api/open/message/deleteMessage":
		method := "GET"
		if strings.HasSuffix(p, "deleteMessage") {
			method = "DELETE"
		}
		if e = methodIs(r, method); e != nil {
			return e
		}
		id := r.URL.Query().Get("shortCode")
		if id == "" {
			return bad("缺少shortCode")
		}
		var created int64
		if e = s.store.QueryRow("SELECT created_at FROM messages m WHERE id=? AND key_id=? AND user_id=? AND protocol='pushplus' AND NOT EXISTS(SELECT 1 FROM compat_hidden_messages h WHERE h.key_id=m.key_id AND h.message_id=m.id)", id, key.ID, key.UserID).Scan(&created); e != nil {
			return &AppError{404, "not_found", "消息不存在"}
		}
		if method == "DELETE" {
			_, e = s.store.Exec("INSERT OR IGNORE INTO compat_hidden_messages VALUES(?,?)", key.ID, id)
			if e != nil {
				return e
			}
			respond(w, 200, params{"code": 200, "msg": "请求成功", "extension": params{"scope": "local_history", "retracted": false}})
			return nil
		}
		var total, pending, failed, attempted int
		if e = s.store.QueryRow("SELECT count(*),coalesce(sum(status='pending'),0),coalesce(sum(status='failed'),0),coalesce(sum(attempts>0),0) FROM deliveries WHERE message_id=?", id).Scan(&total, &pending, &failed, &attempted); e != nil {
			return e
		}
		var routes int
		if e = s.store.QueryRow("SELECT count(*) FROM route_attempts a JOIN deliveries d ON d.id=a.delivery_id WHERE d.message_id=?", id).Scan(&routes); e != nil {
			return e
		}
		if routes > 0 {
			attempted++
		}
		if e = s.store.QueryRow("SELECT coalesce(max(created_at),?) FROM delivery_events WHERE message_id=?", created, id).Scan(&created); e != nil {
			return e
		}
		state := 0
		errText := ""
		if pending > 0 {
			if attempted > 0 {
				state = 1
			}
		} else if total == 0 || failed > 0 {
			state = 3
			errText = "存在失败投递，请查看球球逐设备记录"
		} else {
			state = 2
		}
		var received int
		if e = s.store.QueryRow("SELECT count(DISTINCT device_id) FROM receipts WHERE message_id=? AND stage='received'", id).Scan(&received); e != nil {
			return e
		}
		respond(w, 200, params{"code": 200, "msg": "请求成功", "data": params{"status": state, "errorMessage": errText, "updateTime": compatDate(created)}, "extension": params{"received_devices": received, "target_devices": total, "failed_devices": failed, "pending_devices": pending, "meaning": "status为服务端处理结果，received_devices为APP实际接收回执"}})
		return nil
	default:
		return &AppError{404, "not_found", "接口不存在"}
	}
}

func compatDate(n int64) string { return time.Unix(n, 0).UTC().Format("2006-01-02 15:04:05") }

func (s *Server) pushPlusAccess(w http.ResponseWriter, r *http.Request) error {
	if e := methodIs(r, "POST"); e != nil {
		return e
	}
	var p struct {
		Token  string `json:"token"`
		Secret string `json:"secretKey"`
	}
	if e := decode(r, &p); e != nil {
		return e
	}
	k, e := s.store.key(p.Token, "pushplus")
	if e != nil {
		return e
	}
	var hash, ips string
	if e = s.store.QueryRow("SELECT secret_hash,safe_ips FROM compat_access_settings WHERE key_id=?", k.ID).Scan(&hash, &ips); e != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(digest(p.Secret))) != 1 {
		return &AppError{401, "unauthorized", "开放接口未启用或secretKey无效"}
	}
	var allowed []string
	if json.Unmarshal([]byte(ips), &allowed) != nil || !safePeer(r, allowed) {
		return forbidden()
	}
	token := secret("ppa_")
	now := time.Now().Unix()
	tx, e := s.store.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM compat_access_tokens WHERE expires<=?", now); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE compat_access_tokens SET expires=min(expires,?) WHERE key_id=?", now+300, k.ID); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO compat_access_tokens VALUES(?,?,?)", digest(token), k.ID, now+7200); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	respond(w, 200, params{"code": 200, "msg": "请求成功", "data": params{"accessKey": token, "expiresIn": 7200}})
	return nil
}

func (s *Server) pushPlusAccessKey(r *http.Request) (Key, error) {
	var k Key
	var targets, ips string
	e := s.store.QueryRow("SELECT k.id,k.user_id,k.kind,k.targets,c.safe_ips FROM compat_access_tokens a JOIN api_keys k ON k.id=a.key_id JOIN users u ON u.id=k.user_id JOIN compat_access_settings c ON c.key_id=k.id WHERE a.hash=? AND a.expires>? AND k.disabled=0 AND u.disabled=0 AND k.kind='pushplus'", digest(r.Header.Get("access-key")), time.Now().Unix()).Scan(&k.ID, &k.UserID, &k.Kind, &targets, &ips)
	if e != nil {
		return k, &AppError{401, "unauthorized", "AccessKey无效或已过期"}
	}
	var allowed []string
	if json.Unmarshal([]byte(ips), &allowed) != nil || !safePeer(r, allowed) {
		return k, forbidden()
	}
	e = json.Unmarshal([]byte(targets), &k.Targets)
	return k, e
}

func (s *Server) wxDeleteContent(w http.ResponseWriter, r *http.Request) error {
	k, e := s.store.key(r.URL.Query().Get("appToken"), "wxpusher")
	if e != nil {
		return e
	}
	id, e := strconv.ParseInt(r.URL.Query().Get("messageContentId"), 10, 64)
	if e != nil || id < 1 {
		return bad("messageContentId无效")
	}
	var message string
	if e = s.store.QueryRow("SELECT id FROM messages WHERE rowid=? AND key_id=? AND user_id=? AND protocol='wxpusher'", id, k.ID, k.UserID).Scan(&message); e != nil {
		return &AppError{404, "not_found", "消息不存在"}
	}
	_, e = s.store.Exec("INSERT OR IGNORE INTO compat_deleted_contents VALUES(?,?)", k.ID, message)
	if e != nil {
		return e
	}
	respond(w, 200, params{"code": 1000, "msg": "消息落地页内容已删除，已到达的通知及手机记录不能撤回", "success": true})
	return nil
}

func (s *Server) compatibilityContent(w http.ResponseWriter, r *http.Request) error {
	if e := methodIs(r, "GET"); e != nil {
		return e
	}
	protocol := "pushplus"
	prefix := "/shortMessage/"
	if strings.HasPrefix(r.URL.Path, "/wxpusher/") {
		protocol = "wxpusher"
		prefix = "/wxpusher/message/"
	}
	id := strings.TrimPrefix(r.URL.Path, prefix)
	var payload string
	var hidden, deleted int
	e := s.store.QueryRow("SELECT m.payload,EXISTS(SELECT 1 FROM compat_hidden_messages h WHERE h.key_id=m.key_id AND h.message_id=m.id),EXISTS(SELECT 1 FROM compat_deleted_contents d WHERE d.key_id=m.key_id AND d.message_id=m.id) FROM messages m JOIN api_keys k ON k.id=m.key_id JOIN users u ON u.id=m.user_id WHERE m.id=? AND m.protocol=? AND k.disabled=0 AND u.disabled=0", id, protocol).Scan(&payload, &hidden, &deleted)
	if e == sql.ErrNoRows {
		return &AppError{404, "not_found", "消息不存在"}
	}
	if e != nil {
		return e
	}
	if hidden != 0 || deleted != 0 {
		return &AppError{410, "content_deleted", "消息内容已删除"}
	}
	var m Message
	if e = json.Unmarshal([]byte(payload), &m); e != nil {
		return e
	}
	if m.Expires > 0 && m.Expires <= time.Now().Unix() {
		return &AppError{410, "expired", "消息已过期"}
	}
	body := "<pre>" + html.EscapeString(m.Body) + "</pre>"
	if m.ContentType == "html" {
		body = htmlPolicy.Sanitize(m.Body)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox; frame-ancestors 'none'")
	_, e = w.Write([]byte("<!doctype html><meta name=viewport content='width=device-width, initial-scale=1'><title>球球推送</title><style>body{max-width:48rem;margin:2rem auto;padding:1rem;font:18px/1.6 sans-serif}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><h1>" + html.EscapeString(m.Title) + "</h1>" + body))
	return e
}
