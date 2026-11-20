package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// A native credential is an ntfy token with real upstream ACLs. No global token is exposed.
func nativeTopic(uid string, id int64) string { return fmt.Sprintf("qqg_%s_%d", digest(uid)[:12], id) }
func (s *Server) issueNativeKey(ctx context.Context, uid, label string, t Targets) (map[string]any, error) {
	if e := s.store.ownedTargets(uid, t); e != nil {
		return nil, e
	}
	id := uuid()
	username := "k_" + strings.ReplaceAll(id, "-", "")
	if e := s.ntfyRequest(ctx, "POST", "/v1/users", map[string]string{"username": username, "password": secret("")}, nil, true); e != nil {
		return nil, e
	}
	cleanup := func() {
		_ = s.ntfyRequest(context.WithoutCancel(ctx), "DELETE", "/v1/users", map[string]string{"username": username}, nil, true)
	}
	routes := []map[string]any{}
	for _, id := range t.Topics {
		topic := nativeTopic(uid, id)
		if e := s.ntfyRequest(ctx, "PUT", "/v1/users/access", map[string]string{"username": username, "topic": topic, "permission": "write-only"}, nil, true); e != nil {
			cleanup()
			return nil, e
		}
		var name string
		if e := s.store.QueryRow("SELECT name FROM topics WHERE id=? AND user_id=?", id, uid).Scan(&name); e != nil {
			cleanup()
			return nil, e
		}
		routes = append(routes, map[string]any{"topic": topic, "topic_id": id, "name": name})
	}
	devices, e := s.store.devicesForTargets(uid, Targets{Devices: t.Devices})
	if e != nil {
		cleanup()
		return nil, e
	}
	for _, d := range devices {
		if e = s.ntfyRequest(ctx, "PUT", "/v1/users/access", map[string]string{"username": username, "topic": d.Inbox, "permission": "write-only"}, nil, true); e != nil {
			cleanup()
			return nil, e
		}
		routes = append(routes, map[string]any{"topic": d.Inbox, "device_id": d.ID, "name": d.Name})
	}
	var response struct {
		Token string `json:"token"`
	}
	if e = s.ntfyRequest(ctx, "POST", "/v1/users/tokens", map[string]string{"username": username, "label": label}, &response, true); e != nil {
		cleanup()
		return nil, e
	}
	if response.Token == "" {
		cleanup()
		return nil, fmt.Errorf("empty ntfy token")
	}
	_, e = s.store.Exec("INSERT INTO api_keys(id,user_id,kind,label,hash,hint,targets,created_at) VALUES(?,?,'ntfy',?,?,?,?,?)", id, uid, label, digest(response.Token), "tk_…"+response.Token[len(response.Token)-4:], encode(t), time.Now().Unix())
	if e != nil {
		cleanup()
		return nil, e
	}
	return map[string]any{"id": id, "token": response.Token, "kind": "ntfy", "native_topics": routes, "base_url": s.cfg.NtfyPublicURL}, nil
}
func (s *Server) syncNativeRead(ctx context.Context, uid, device string, topicID int64, remove bool) error {
	var username string
	if e := s.store.QueryRow("SELECT ntfy_user FROM devices WHERE id=? AND user_id=? AND disabled=0", device, uid).Scan(&username); e != nil {
		return forbidden()
	}
	method := "PUT"
	if remove {
		method = "DELETE"
	}
	return s.ntfyRequest(ctx, method, "/v1/users/access", map[string]string{"username": username, "topic": nativeTopic(uid, topicID), "permission": "read-only"}, nil, true)
}
func (s *Server) recordNative(r *http.Request, raw []byte) error {
	var original params
	if e := json.Unmarshal(raw, &original); e != nil || original.str("event") != "message" {
		return e
	}
	_, e := s.store.Exec("INSERT OR IGNORE INTO native_publications(ntfy_id,payload,created_at) VALUES(?,?,?)", original.str("id"), string(raw), time.Now().Unix())
	if e != nil {
		return e
	}
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return nil
	}
	key, e := s.store.key(strings.TrimPrefix(authorization, "Bearer "), "ntfy")
	if e != nil {
		return nil
	}
	topic := original.str("topic")
	m := &Message{ID: uuid(), UserID: key.UserID, Protocol: "ntfy", Business: "custom", Icon: "custom", Sound: "default", Title: original.str("title"), Body: original.str("message"), Click: original.str("click"), Priority: original.num("priority"), Tags: original.list("tags"), CreatedAt: time.Now().Unix(), ContentType: "text", Ntfy: original, Sequence: first(original.str("sequence_id"), original.str("id"))}
	if original.str("content_type") == "text/markdown" {
		m.ContentType = "markdown"
	}
	for _, id := range key.Targets.Topics {
		if topic == nativeTopic(key.UserID, id) {
			m.TopicIDs = []int64{id}
			devices, e := s.store.resolve(key, m)
			if e != nil {
				return e
			}
			if _, e = s.store.accept(key, m, devices, ""); e != nil {
				return e
			}
			select {
			case s.wake <- struct{}{}:
			default:
			}
			return nil
		}
	}
	devices, e := s.store.devicesForTargets(key.UserID, Targets{Devices: key.Targets.Devices})
	if e != nil {
		return e
	}
	for _, d := range devices {
		if d.Inbox == topic {
			// The original publication already reached the private inbox; record it without sending it again.
			tx, e := s.store.Begin()
			if e != nil {
				return e
			}
			defer tx.Rollback()
			_, e = tx.Exec("INSERT INTO messages(id,user_id,key_id,protocol,payload,status,created_at) VALUES(?,?,?,'ntfy',?,'published',?)", m.ID, key.UserID, key.ID, encode(m), m.CreatedAt)
			if e != nil {
				return e
			}
			_, e = tx.Exec("INSERT INTO deliveries(message_id,device_id,topic,ntfy_id,status,attempts) VALUES(?,?,?,?,'published',1)", m.ID, d.ID, topic, original.str("id"))
			if e != nil {
				return e
			}
			return tx.Commit()
		}
	}
	return nil
}
