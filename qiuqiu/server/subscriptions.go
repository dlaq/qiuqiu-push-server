package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
)

var publicHostPattern = regexp.MustCompile(`^[a-zA-Z0-9.-]+(?::[0-9]+)?$`)

func (s *Server) qrKey(code string) (Key, string, error) {
	var k Key
	var extra, targets string
	e := s.store.QueryRow("SELECT k.id,k.user_id,k.kind,k.targets,q.extra FROM wx_qr q JOIN api_keys k ON k.id=q.key_id JOIN users u ON u.id=k.user_id WHERE q.hash=? AND q.expires>? AND k.disabled=0 AND u.disabled=0", digest(code), time.Now().Unix()).Scan(&k.ID, &k.UserID, &k.Kind, &targets, &extra)
	if e != nil {
		return k, "", &AppError{404, "not_found", "订阅码无效或已过期"}
	}
	e = json.Unmarshal([]byte(targets), &k.Targets)
	return k, extra, e
}

func (s *Server) wxQRHTTP(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/fun/qrcode/") {
		if e := methodIs(r, "GET"); e != nil {
			return e
		}
		code := strings.TrimSuffix(strings.TrimPrefix(p, "/api/fun/qrcode/"), ".png")
		if _, _, e := s.qrKey(code); e != nil {
			return e
		}
		if !publicHostPattern.MatchString(r.Host) {
			return bad("Host无效")
		}
		png, e := qrcode.Encode("https://"+r.Host+"/api/s/"+code, qrcode.Medium, 320)
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, e = w.Write(png)
		return e
	}
	if p == "/api/fun/scan-qrcode-uid" {
		if e := methodIs(r, "GET"); e != nil {
			return e
		}
		code := r.URL.Query().Get("code")
		if _, _, e := s.qrKey(code); e != nil {
			return e
		}
		var uid string
		if e := s.store.QueryRow("SELECT last_uid FROM wx_qr WHERE hash=?", digest(code)).Scan(&uid); e != nil {
			return e
		}
		respond(w, 200, params{"code": 1000, "msg": "处理成功", "success": true, "data": uid})
		return nil
	}
	if p != "/api/fun/create/qrcode" {
		return &AppError{404, "not_found", "接口不存在"}
	}
	if e := methodIs(r, "POST"); e != nil {
		return e
	}
	var in struct {
		Token     string `json:"appToken"`
		Extra     string `json:"extra"`
		ValidTime int64  `json:"validTime"`
	}
	if e := decode(r, &in); e != nil {
		return e
	}
	k, e := s.store.key(in.Token, "wxpusher")
	if e != nil {
		return e
	}
	if in.ValidTime == 0 {
		in.ValidTime = 1800
	}
	if in.Extra == "" || utf8.RuneCountInString(in.Extra) > 64 || in.ValidTime < 1 || in.ValidTime > 2592000 {
		return bad("extra为1–64字符，有效期1–2592000秒")
	}
	if !publicHostPattern.MatchString(r.Host) {
		return bad("Host无效")
	}
	code := secret("wqs_")
	now := time.Now().Unix()
	tx, e := s.store.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM wx_qr WHERE expires<=?", now); e != nil {
		return e
	}
	var count int
	if e = tx.QueryRow("SELECT count(*) FROM wx_qr WHERE key_id=?", k.ID).Scan(&count); e != nil {
		return e
	}
	if count >= 1000 {
		return &AppError{429, "rate_limited", "有效订阅码过多，请等待过期"}
	}
	if _, e = tx.Exec("INSERT INTO wx_qr(hash,key_id,extra,expires) VALUES(?,?,?,?)", digest(code), k.ID, in.Extra, now+in.ValidTime); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	base := "https://" + r.Host
	respond(w, 200, params{"code": 1000, "msg": "处理成功", "success": true, "data": params{"code": code, "expires": in.ValidTime, "url": base + "/api/fun/qrcode/" + code + ".png", "link": base + "/api/s/" + code}, "extension": params{"authentication": "local_owner_session", "meaning": "登录本地账户后确认设备订阅，不创建公开账户、不连接微信"}})
	return nil
}

// Closed registration is preserved: scanning alone never binds a device or
// grants access. Its owner must sign in and explicitly confirm the subscription.
func (s *Server) wxSubscribe(w http.ResponseWriter, r *http.Request, u User, resource string) error {
	var in struct {
		Code   string  `json:"code"`
		Device string  `json:"device_id"`
		Topics []int64 `json:"topic_ids"`
	}
	if e := methodIs(r, "POST"); e != nil {
		return e
	}
	if e := decode(r, &in); e != nil {
		return e
	}
	k, extra, e := s.qrKey(in.Code)
	if e != nil {
		return e
	}
	if k.UserID != u.ID {
		return forbidden()
	}
	if resource == "subscriptions/preview" {
		devices, e := s.store.list("SELECT id,name FROM devices WHERE user_id=? AND disabled=0", u.ID)
		if e != nil {
			return e
		}
		topics := []params{}
		for _, id := range k.Targets.Topics {
			var name string
			if e = s.store.QueryRow("SELECT name FROM topics WHERE id=? AND user_id=?", id, u.ID).Scan(&name); e != nil {
				return e
			}
			topics = append(topics, params{"id": id, "name": name})
		}
		respond(w, 200, params{"extra": extra, "devices": devices, "topic_ids": k.Targets.Topics, "topics": topics, "device_ids": k.Targets.Devices})
		return nil
	}
	if len(in.Topics) > len(k.Targets.Topics) {
		return forbidden()
	}
	var n int
	if e = s.store.QueryRow("SELECT count(*) FROM devices WHERE id=? AND user_id=? AND disabled=0", in.Device, u.ID).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return forbidden()
	}
	direct := false
	for _, id := range k.Targets.Devices {
		if id == in.Device {
			direct = true
		}
	}
	if len(in.Topics) == 0 && !direct {
		return bad("请选择订阅主题；此设备不在直接授权列表中")
	}
	for _, id := range in.Topics {
		found := false
		for _, allowed := range k.Targets.Topics {
			if id == allowed {
				found = true
			}
		}
		if !found {
			return forbidden()
		}
	}
	tx, e := s.store.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	// Recheck expiry and revocation inside the same transaction as binding.
	if e = tx.QueryRow("SELECT count(*) FROM wx_qr q JOIN api_keys k ON k.id=q.key_id JOIN users u ON u.id=k.user_id WHERE q.hash=? AND q.expires>? AND k.disabled=0 AND u.disabled=0", digest(in.Code), time.Now().Unix()).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return forbidden()
	}
	if e = tx.QueryRow("SELECT count(*) FROM devices WHERE id=? AND user_id=? AND disabled=0", in.Device, u.ID).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return forbidden()
	}
	for _, id := range in.Topics {
		if _, e = tx.Exec("INSERT OR IGNORE INTO topic_devices VALUES(?,?)", id, in.Device); e != nil {
			return e
		}
	}
	if _, e = tx.Exec("UPDATE wx_qr SET last_uid=? WHERE hash=?", in.Device, digest(in.Code)); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.store.audit(u.ID, "subscription_confirmed", "设备 "+in.Device)
	respond(w, 200, params{"success": true, "uid": in.Device, "extra": extra, "message": "订阅已确认，可用本地设备UID发送"})
	return nil
}
