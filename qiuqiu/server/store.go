package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type Store struct{ *sql.DB }
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}
type Device struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Inbox    string `json:"inbox"`
	NtfyUser string `json:"ntfy_user"`
	Disabled bool   `json:"disabled"`
}

func openStore(path string) (*Store, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(schema); e != nil {
		db.Close()
		return nil, e
	}
	if _, e = db.Exec("PRAGMA busy_timeout=5000"); e != nil {
		return nil, e
	}
	if _, e = db.Exec(trackingSchema); e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) addUser(name, password, role string) (User, error) {
	u := User{ID: "UID_" + strings.ReplaceAll(uuid(), "-", ""), Name: strings.TrimSpace(name), Role: role}
	if len(u.Name) < 1 || len(u.Name) > 80 || len(password) < 12 || len(password) > 72 {
		return u, bad("用户名为 1–80 字符，密码为 12–72 字节")
	}
	if role != "admin" && role != "user" {
		return u, bad("角色无效")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return u, e
	}
	_, e = s.Exec("INSERT INTO users(id,name,password_hash,role) VALUES(?,?,?,?)", u.ID, u.Name, string(h), role)
	return u, e
}
func (s *Store) login(name, password string) (User, string, error) {
	var u User
	var h string
	e := s.QueryRow("SELECT id,name,password_hash,role,disabled FROM users WHERE name=?", name).Scan(&u.ID, &u.Name, &h, &u.Role, &u.Disabled)
	if e != nil || u.Disabled || bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) != nil {
		return u, "", &AppError{401, "unauthorized", "用户名或密码错误"}
	}
	token := secret("qs_")
	_, e = s.Exec("INSERT INTO sessions VALUES(?,?,?)", digest(token), u.ID, time.Now().Add(12*time.Hour).Unix())
	return u, token, e
}
func (s *Store) session(token string) (User, error) {
	var u User
	e := s.QueryRow("SELECT u.id,u.name,u.role,u.disabled FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.hash=? AND s.expires>? AND u.disabled=0", digest(token), time.Now().Unix()).Scan(&u.ID, &u.Name, &u.Role, &u.Disabled)
	if e != nil {
		return u, &AppError{401, "unauthorized", "请重新登录"}
	}
	return u, nil
}
func sc3UID(uid string) string {
	return strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(uid))), 10)
}
func (s *Store) key(token, kind string) (Key, error) {
	var k Key
	var targets string
	e := s.QueryRow("SELECT k.id,k.user_id,k.kind,k.targets FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.hash=? AND k.kind=? AND k.disabled=0 AND u.disabled=0", digest(token), kind).Scan(&k.ID, &k.UserID, &k.Kind, &targets)
	if e != nil {
		return k, &AppError{401, "unauthorized", "凭证无效或已禁用"}
	}
	e = json.Unmarshal([]byte(targets), &k.Targets)
	return k, e
}
func (s *Store) ownedTargets(uid string, t Targets) error {
	if len(t.Devices)+len(t.Topics) == 0 {
		return bad("至少绑定一个设备或 Topic")
	}
	if len(t.Devices)+len(t.Topics) > 2000 {
		return bad("目标过多")
	}
	for _, d := range t.Devices {
		var n int
		if e := s.QueryRow("SELECT count(*) FROM devices WHERE id=? AND user_id=? AND disabled=0", d, uid).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return forbidden()
		}
	}
	for _, t := range t.Topics {
		var n int
		if e := s.QueryRow("SELECT count(*) FROM topics WHERE id=? AND user_id=?", t, uid).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return forbidden()
		}
	}
	return nil
}
func (s *Store) issueKey(uid, app, kind, label string, t Targets) (map[string]any, error) {
	if e := s.ownedTargets(uid, t); e != nil {
		return nil, e
	}
	if app != "" {
		var owner string
		if s.QueryRow("SELECT user_id FROM apps WHERE id=?", app).Scan(&owner) != nil || owner != uid {
			return nil, forbidden()
		}
	}
	prefix := map[string]string{"native": "qq_", "serverchan": "SCT", "sc3": "sctp" + sc3UID(uid) + "t", "pushplus": "pp_", "bark": "bk_", "wxpusher": "AT_", "wxpusher_spt": "SPT_"}[kind]
	if prefix == "" {
		return nil, bad("未知凭证类型")
	}
	token := secret(prefix)
	id := uuid()
	var appArg any
	if app != "" {
		appArg = app
	}
	_, e := s.Exec("INSERT INTO api_keys VALUES(?,?,?,?,?,?,?,?,0,?)", id, uid, appArg, kind, label, digest(token), token[:len(prefix)]+"…"+token[len(token)-4:], encode(t), time.Now().Unix())
	return map[string]any{"id": id, "token": token, "kind": kind, "sc3_uid": sc3UID(uid)}, e
}
func (s *Store) devicesForTargets(uid string, t Targets) (map[string]Device, error) {
	out := map[string]Device{}
	for _, id := range t.Devices {
		var d Device
		if e := s.QueryRow("SELECT id,user_id,name,inbox,ntfy_user,disabled FROM devices WHERE id=? AND user_id=? AND disabled=0", id, uid).Scan(&d.ID, &d.UserID, &d.Name, &d.Inbox, &d.NtfyUser, &d.Disabled); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			return nil, e
		}
		out[d.ID] = d
	}
	for _, id := range t.Topics {
		rows, e := s.Query("SELECT d.id,d.user_id,d.name,d.inbox,d.ntfy_user,d.disabled FROM topic_devices td JOIN devices d ON d.id=td.device_id JOIN topics t ON t.id=td.topic_id WHERE t.id=? AND t.user_id=? AND d.user_id=? AND d.disabled=0", id, uid, uid)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var d Device
			if e = rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Inbox, &d.NtfyUser, &d.Disabled); e != nil {
				rows.Close()
				return nil, e
			}
			out[d.ID] = d
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (s *Store) resolve(k Key, m *Message) ([]Device, error) {
	for i, id := range m.Devices {
		if !strings.HasPrefix(id, "DEV_") {
			var resolved string
			if e := s.QueryRow("SELECT d.id FROM devices d JOIN device_profiles p ON p.device_id=d.id WHERE d.user_id=? AND p.alias=? AND d.disabled=0", k.UserID, k.UserID+":"+id).Scan(&resolved); e != nil {
				return nil, forbidden()
			}
			m.Devices[i] = resolved
		}
	}
	if len(m.Devices)+len(m.UIDs)+len(m.TopicIDs) > 2000 {
		return nil, bad("目标过多")
	}
	allowed, e := s.devicesForTargets(k.UserID, k.Targets)
	if e != nil {
		return nil, e
	}
	requested := Targets{Devices: append([]string{}, m.Devices...), Topics: append([]int64{}, m.TopicIDs...)}
	for _, uid := range m.UIDs {
		if uid == k.UserID {
			for _, d := range allowed {
				requested.Devices = append(requested.Devices, d.ID)
			}
		} else if _, ok := allowed[uid]; ok {
			requested.Devices = append(requested.Devices, uid)
		} else {
			return nil, forbidden()
		}
	}
	if m.Topic != "" {
		var id int64
		if s.QueryRow("SELECT id FROM topics WHERE name=? AND user_id=?", m.Topic, k.UserID).Scan(&id) != nil {
			return nil, forbidden()
		}
		requested.Topics = append(requested.Topics, id)
	}
	if len(m.Devices)+len(m.UIDs)+len(m.TopicIDs) == 0 && m.Topic == "" {
		requested = k.Targets
	}
	// A topic target must itself be in scope; device membership alone must not authorize another topic.
	for _, id := range requested.Topics {
		found := false
		for _, v := range k.Targets.Topics {
			if id == v {
				found = true
			}
		}
		if !found {
			return nil, forbidden()
		}
	}
	for _, id := range requested.Devices {
		if _, ok := allowed[id]; !ok {
			return nil, forbidden()
		}
	}
	selected, e := s.devicesForTargets(k.UserID, requested)
	if e != nil {
		return nil, e
	}
	if len(selected) == 0 {
		return nil, bad("没有可投递的已绑定设备")
	}
	out := []Device{}
	for _, d := range selected {
		if _, ok := allowed[d.ID]; !ok {
			return nil, forbidden()
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Store) accept(k Key, m *Message, devices []Device, idem string) (*Message, error) {
	m.UserID = k.UserID
	if len(idem) > 128 {
		return nil, bad("Idempotency-Key 过长")
	}
	hashMessage := *m
	hashMessage.ID = ""
	hashMessage.ContentID = 0
	hashMessage.CreatedAt = 0
	fingerprint := digest(encode(hashMessage))
	tx, e := s.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if idem != "" {
		var payload, previous string
		e = tx.QueryRow("SELECT payload,request_hash FROM messages WHERE key_id=? AND idempotency=?", k.ID, idem).Scan(&payload, &previous)
		if e == nil {
			if previous != fingerprint {
				return nil, &AppError{409, "idempotency_conflict", "相同 Idempotency-Key 对应不同请求"}
			}
			var old Message
			if e = json.Unmarshal([]byte(payload), &old); e != nil {
				return nil, e
			}
			return &old, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
	}
	var idemValue any
	if idem != "" {
		idemValue = idem
	}
	result, e := tx.Exec("INSERT INTO messages(id,user_id,key_id,protocol,payload,status,created_at,idempotency,request_hash) VALUES(?,?,?,?,?,'accepted',?,?,?)", m.ID, k.UserID, k.ID, m.Protocol, encode(m), m.CreatedAt, idemValue, fingerprint)
	if e != nil {
		return nil, e
	}
	m.ContentID, e = result.LastInsertId()
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec("UPDATE messages SET payload=? WHERE id=?", encode(m), m.ID); e != nil {
		return nil, e
	}
	for _, d := range devices {
		if _, e = tx.Exec("INSERT INTO deliveries(message_id,device_id,topic) VALUES(?,?,?)", m.ID, d.ID, d.Inbox); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return m, nil
}
func (s *Store) audit(uid, event, detail string) {
	_, _ = s.Exec("INSERT INTO audit(user_id,event,detail,created_at) VALUES(?,?,?,?)", uid, event, detail, time.Now().Unix())
}
func (s *Store) list(query string, args ...any) ([]map[string]any, error) {
	rows, e := s.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if e = rows.Scan(p...); e != nil {
			return nil, e
		}
		item := map[string]any{}
		for i, c := range cols {
			item[c] = v[i]
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func checkRows(result sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e == nil && n == 0 {
		return &AppError{404, "not_found", "记录不存在"}
	}
	return e
}
func dbError(e error) error {
	if e == nil {
		return nil
	}
	if strings.Contains(e.Error(), "UNIQUE constraint") {
		return &AppError{409, "conflict", "记录已存在"}
	}
	var a *AppError
	if errors.As(e, &a) {
		return a
	}
	return fmt.Errorf("storage operation: %w", e)
}

func (s *Store) usage(k Key) {
	_, _ = s.Exec("INSERT INTO api_usage(key_id,user_id,protocol,calls) VALUES(?,?,?,1) ON CONFLICT(key_id) DO UPDATE SET calls=calls+1", k.ID, k.UserID, k.Kind)
}
