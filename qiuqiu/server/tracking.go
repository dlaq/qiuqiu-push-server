package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Separate tables keep the v1 positional inserts and historical databases compatible.
const trackingSchema = `
CREATE TABLE IF NOT EXISTS device_profiles(device_id TEXT PRIMARY KEY REFERENCES devices(id),alias TEXT NOT NULL DEFAULT '',mode TEXT NOT NULL DEFAULT 'fcm',app_version TEXT NOT NULL DEFAULT '',last_seen INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS device_alias_unique ON device_profiles(alias) WHERE alias<>'';
CREATE TABLE IF NOT EXISTS receipts(message_id TEXT NOT NULL REFERENCES messages(id),device_id TEXT NOT NULL REFERENCES devices(id),stage TEXT NOT NULL,event_time INTEGER NOT NULL,received_at INTEGER NOT NULL,reason TEXT NOT NULL DEFAULT '',PRIMARY KEY(message_id,device_id,stage));
CREATE TABLE IF NOT EXISTS delivery_events(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id TEXT NOT NULL,message_id TEXT NOT NULL,device_id TEXT NOT NULL DEFAULT '',channel TEXT NOT NULL DEFAULT 'ntfy_fcm',stage TEXT NOT NULL,created_at INTEGER NOT NULL,detail TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS events_user_cursor ON delivery_events(user_id,id);
CREATE INDEX IF NOT EXISTS events_message_cursor ON delivery_events(message_id,id);
CREATE TRIGGER IF NOT EXISTS message_accepted_event AFTER INSERT ON messages BEGIN
 INSERT INTO delivery_events(user_id,message_id,stage,created_at) VALUES(NEW.user_id,NEW.id,NEW.status,unixepoch()); END;
CREATE TRIGGER IF NOT EXISTS delivery_insert_event AFTER INSERT ON deliveries BEGIN
 INSERT INTO delivery_events(user_id,message_id,device_id,stage,created_at) SELECT user_id,NEW.message_id,coalesce(NEW.device_id,''),NEW.status,unixepoch() FROM messages WHERE id=NEW.message_id; END;
CREATE TRIGGER IF NOT EXISTS delivery_update_event AFTER UPDATE OF status,attempts ON deliveries WHEN OLD.status<>NEW.status OR OLD.attempts<>NEW.attempts BEGIN
 INSERT INTO delivery_events(user_id,message_id,device_id,stage,created_at,detail) SELECT user_id,NEW.message_id,coalesce(NEW.device_id,''),NEW.status,unixepoch(),NEW.error FROM messages WHERE id=NEW.message_id; END;
CREATE TRIGGER IF NOT EXISTS receipt_event AFTER INSERT ON receipts BEGIN
 INSERT INTO delivery_events(user_id,message_id,device_id,channel,stage,created_at,detail) SELECT user_id,NEW.message_id,NEW.device_id,'device',NEW.stage,NEW.received_at,NEW.reason FROM messages WHERE id=NEW.message_id; END;
INSERT OR IGNORE INTO schema_version VALUES(2);
`

type Receipt struct {
	MessageID string `json:"message_id"`
	Stage     string `json:"stage"`
	EventTime int64  `json:"event_time"`
	Reason    string `json:"reason"`
}

func (s *Server) deviceAuth(token string) (Device, error) {
	var d Device
	err := s.store.QueryRow("SELECT d.id,d.user_id,d.name,d.inbox,d.ntfy_user FROM devices d JOIN users u ON u.id=d.user_id WHERE d.token_hash=? AND d.disabled=0 AND u.disabled=0", digest(token)).Scan(&d.ID, &d.UserID, &d.Name, &d.Inbox, &d.NtfyUser)
	if err != nil {
		return d, &AppError{401, "device_unauthorized", "设备凭证无效或已撤销"}
	}
	return d, nil
}

func (s *Server) deviceHTTP(w http.ResponseWriter, r *http.Request) error {
	if r.Method != "POST" {
		return &AppError{405, "method_not_allowed", "需要 POST"}
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.URL.Path == "/api/v1/device/upgrade" {
		return s.upgradeDevice(w, r, token)
	}
	d, err := s.deviceAuth(token)
	if err != nil {
		return err
	}
	if r.URL.Path == "/api/v1/device/profile" {
		var p struct {
			Mode    string `json:"mode"`
			Version string `json:"app_version"`
		}
		if err = decode(r, &p); err != nil {
			return err
		}
		if p.Mode != "fcm" && p.Mode != "direct" && p.Mode != "auto" {
			return bad("接收模式无效")
		}
		_, err = s.store.Exec("INSERT INTO device_profiles(device_id,mode,app_version,last_seen) VALUES(?,?,?,?) ON CONFLICT(device_id) DO UPDATE SET mode=excluded.mode,app_version=excluded.app_version,last_seen=excluded.last_seen", d.ID, p.Mode, p.Version, time.Now().Unix())
		if err == nil {
			respond(w, 200, params{"success": true})
		}
		return err
	}
	if r.URL.Path != "/api/v1/device/receipts" {
		return &AppError{404, "not_found", "接口不存在"}
	}
	var p struct {
		Receipts []Receipt `json:"receipts"`
	}
	if err = decode(r, &p); err != nil {
		return err
	}
	if len(p.Receipts) == 0 || len(p.Receipts) > 100 {
		return bad("每批回执为1–100条")
	}
	tx, err := s.store.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	results := []params{}
	for index, a := range p.Receipts {
		if a.Stage != "received" && a.Stage != "notification_submitted" && a.Stage != "notification_blocked" && a.Stage != "notification_suppressed" && a.Stage != "opened" {
			return bad("回执阶段无效")
		}
		if len(a.Reason) > 500 {
			return bad("回执说明过长")
		}
		var n int
		if err = tx.QueryRow("SELECT count(*) FROM deliveries v JOIN messages m ON m.id=v.message_id WHERE v.message_id=? AND v.device_id=? AND m.user_id=?", a.MessageID, d.ID, d.UserID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			var unified string
			if err = tx.QueryRow("SELECT v.message_id FROM deliveries v JOIN messages m ON m.id=v.message_id WHERE (v.ntfy_id=? OR EXISTS(SELECT 1 FROM transport_ids t WHERE t.delivery_id=v.id AND t.transport_id=?)) AND v.device_id=? AND m.user_id=?", a.MessageID, a.MessageID, d.ID, d.UserID).Scan(&unified); err != nil {
				if err != sql.ErrNoRows {
					return err
				}
				// Untracked native messages and expired records must not poison a
				// batch containing valid receipts. Reveal no other device's data.
				results = append(results, params{"index": index, "status": "unknown_message"})
				continue
			}
			a.MessageID = unified
		}
		if a.EventTime == 0 {
			a.EventTime = time.Now().Unix()
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO receipts VALUES(?,?,?,?,?,?)", a.MessageID, d.ID, a.Stage, a.EventTime, time.Now().Unix(), a.Reason); err != nil {
			return err
		}
		results = append(results, params{"index": index, "status": "recorded"})
	}
	if _, err = tx.Exec("INSERT INTO device_profiles(device_id,last_seen) VALUES(?,?) ON CONFLICT(device_id) DO UPDATE SET last_seen=excluded.last_seen", d.ID, time.Now().Unix()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	respond(w, 200, params{"success": true, "results": results})
	return nil
}

// The old APP already owns an ntfy token. Verify its principal before rotating
// the independent Gateway device token; never accept a user-supplied inbox.
func (s *Server) upgradeDevice(w http.ResponseWriter, r *http.Request, token string) error {
	req, err := http.NewRequestWithContext(r.Context(), "GET", s.cfg.NtfyURL+"/v1/account", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return &AppError{503, "ntfy_unavailable", "暂时无法验证设备"}
	}
	defer resp.Body.Close()
	var account struct {
		Username string `json:"username"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&account) != nil || account.Username == "" {
		return &AppError{401, "device_unauthorized", "原设备读取凭证无效"}
	}
	var id string
	if err = s.store.QueryRow("SELECT d.id FROM devices d JOIN users u ON u.id=d.user_id WHERE d.ntfy_user=? AND d.disabled=0 AND u.disabled=0", account.Username).Scan(&id); err != nil {
		return forbidden()
	}
	value := secret("qd_")
	if _, err = s.store.Exec("UPDATE devices SET token_hash=? WHERE id=?", digest(value), id); err != nil {
		return err
	}
	respond(w, 200, params{"device_id": id, "device_token": value})
	return nil
}

func (s *Server) trackingManage(w http.ResponseWriter, r *http.Request, u User, resource string) (bool, error) {
	if r.Method == "PATCH" && resource == "devices/profile" {
		var p struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Alias string `json:"alias"`
		}
		if e := decode(r, &p); e != nil {
			return true, e
		}
		if len(strings.TrimSpace(p.Name)) == 0 || len(p.Name) > 100 {
			return true, bad("设备名称为1–100字节")
		}
		if p.Alias != "" && !topicPattern.MatchString(p.Alias) {
			return true, bad("设备别名使用1–64位字母数字、下划线或连字符")
		}
		tx, e := s.store.Begin()
		if e != nil {
			return true, e
		}
		defer tx.Rollback()
		if e = checkRows(tx.Exec("UPDATE devices SET name=? WHERE id=? AND user_id=?", strings.TrimSpace(p.Name), p.ID, u.ID)); e != nil {
			return true, e
		}
		// Prefix the unique index value with owner ID so aliases are tenant scoped.
		alias := ""
		if p.Alias != "" {
			alias = u.ID + ":" + p.Alias
		}
		if _, e = tx.Exec("INSERT INTO device_profiles(device_id,alias) VALUES(?,?) ON CONFLICT(device_id) DO UPDATE SET alias=excluded.alias", p.ID, alias); e != nil {
			return true, e
		}
		if e = tx.Commit(); e == nil {
			respond(w, 200, params{"success": true})
		}
		return true, e
	}
	if r.Method != "GET" {
		return false, nil
	}
	if resource == "device-profiles" {
		rows, e := s.store.list("SELECT d.id,d.name,coalesce(p.mode,'fcm') mode,coalesce(p.alias,'') alias,coalesce(p.app_version,'') app_version,coalesce(p.last_seen,0) last_seen FROM devices d LEFT JOIN device_profiles p ON p.device_id=d.id WHERE d.user_id=?", u.ID)
		for _, row := range rows {
			row["alias"] = strings.TrimPrefix(row["alias"].(string), u.ID+":")
		}
		if e == nil {
			respond(w, 200, params{"data": rows})
		}
		return true, e
	}
	if resource == "events" {
		return true, s.eventStream(w, r, u)
	}
	if !strings.HasPrefix(resource, "messages/") {
		return false, nil
	}
	id := strings.TrimPrefix(resource, "messages/")
	var raw, state string
	if e := s.store.QueryRow("SELECT payload,status FROM messages WHERE id=? AND user_id=?", id, u.ID).Scan(&raw, &state); e != nil {
		return true, &AppError{404, "not_found", "消息不存在"}
	}
	var m Message
	if e := json.Unmarshal([]byte(raw), &m); e != nil {
		return true, e
	}
	deliveries, e := s.store.list("SELECT v.*,d.name device_name FROM deliveries v LEFT JOIN devices d ON d.id=v.device_id WHERE v.message_id=?", id)
	if e != nil {
		return true, e
	}
	receipts, e := s.store.list("SELECT * FROM receipts WHERE message_id=? ORDER BY received_at", id)
	if e != nil {
		return true, e
	}
	events, e := s.store.list("SELECT * FROM delivery_events WHERE message_id=? ORDER BY id", id)
	if e != nil {
		return true, e
	}
	attempts, e := s.store.list("SELECT a.* FROM route_attempts a JOIN deliveries d ON d.id=a.delivery_id WHERE d.message_id=? ORDER BY a.id", id)
	if e != nil {
		return true, e
	}
	transports, e := s.store.list("SELECT t.* FROM transport_ids t JOIN deliveries d ON d.id=t.delivery_id WHERE d.message_id=? ORDER BY t.created_at", id)
	if e != nil {
		return true, e
	}
	respond(w, 200, params{"message": m, "status": state, "deliveries": deliveries, "receipts": receipts, "events": events, "route_attempts": attempts, "transport_ids": transports, "receipt_timeout_seconds": 60})
	return true, nil
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request, u User) error {
	flush, ok := w.(http.Flusher)
	if !ok {
		return bad("不支持流式响应")
	}
	cursor, _ := strconv.ParseInt(first(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("after")), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ctl := http.NewResponseController(w)
	_ = ctl.SetWriteDeadline(time.Time{})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, e := s.store.session(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); e != nil {
			return nil
		}
		events, e := s.store.list("SELECT * FROM delivery_events WHERE user_id=? AND id>? ORDER BY id LIMIT 200", u.ID, cursor)
		if e != nil {
			return nil
		}
		for _, event := range events {
			cursor = event["id"].(int64)
			if _, e = fmt.Fprintf(w, "id: %d\nevent: delivery\ndata: %s\n\n", cursor, encode(event)); e != nil {
				return nil
			}
		}
		if len(events) == 0 {
			if _, e = fmt.Fprint(w, ": keepalive\n\n"); e != nil {
				return nil
			}
		}
		flush.Flush()
		select {
		case <-r.Context().Done():
			return nil
		case <-ticker.C:
		}
	}
}
