package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return bad("JSON 参数无效")
	}
	return nil
}
func (s *Server) manage(w http.ResponseWriter, r *http.Request) error {
	path := r.URL.Path
	method := r.Method
	if path == "/api/v1/login" {
		if method != "POST" {
			return &AppError{405, "method_not_allowed", "需要 POST"}
		}
		var p struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if e := decode(r, &p); e != nil {
			return e
		}
		u, token, e := s.store.login(p.Name, p.Password)
		if e != nil {
			return e
		}
		respond(w, 200, map[string]any{"token": token, "user": u, "expires_in": 43200})
		return nil
	}
	if path == "/api/v1/pair/claim" {
		if method != "POST" {
			return &AppError{405, "method_not_allowed", "需要 POST"}
		}
		return s.claim(w, r)
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u, e := s.store.session(token)
	if e != nil {
		return e
	}
	if path == "/api/v1/me" && method == "GET" {
		respond(w, 200, map[string]any{"user": u, "sc3_uid": sc3UID(u.ID), "ntfy_url": s.cfg.NtfyPublicURL})
		return nil
	}
	if path == "/api/v1/logout" && method == "POST" {
		_, e = s.store.Exec("DELETE FROM sessions WHERE hash=?", digest(token))
		respond(w, 200, map[string]bool{"success": e == nil})
		return e
	}
	resource := strings.TrimPrefix(path, "/api/v1/manage/")
	if handled, err := s.compatibilityManage(w, r, u, resource); handled {
		return err
	}
	if handled, err := s.uploadIcon(w, r, u); handled {
		return err
	}
	if handled, err := s.barkCryptoManage(w, r, u, resource); handled {
		return err
	}
	if handled, err := s.channelManage(w, r, u, resource); handled {
		return err
	}
	if handled, err := s.trackingManage(w, r, u, resource); handled {
		return err
	}
	if method == "GET" {
		return s.listResource(w, r, u, resource)
	}
	if method == "POST" {
		switch resource {
		case "users":
			if u.Role != "admin" {
				return forbidden()
			}
			var p struct {
				Name     string `json:"name"`
				Password string `json:"password"`
				Role     string `json:"role"`
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			if p.Role == "" {
				p.Role = "user"
			}
			created, e := s.store.addUser(p.Name, p.Password, p.Role)
			if e != nil {
				return e
			}
			respond(w, 201, created)
			return nil
		case "apps":
			var p struct {
				Name string `json:"name"`
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			if len(p.Name) < 1 || len(p.Name) > 100 {
				return bad("应用名称无效")
			}
			id := uuid()
			_, e = s.store.Exec("INSERT INTO apps VALUES(?,?,?)", id, u.ID, p.Name)
			if e != nil {
				return e
			}
			respond(w, 201, map[string]string{"id": id, "name": p.Name})
			return nil
		case "topics":
			var p struct {
				Name string `json:"name"`
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			if !topicPattern.MatchString(p.Name) {
				return bad("Topic 使用1–64位字母数字、下划线或连字符")
			}
			res, e := s.store.Exec("INSERT INTO topics(user_id,name) VALUES(?,?)", u.ID, p.Name)
			if e != nil {
				return e
			}
			id, e := res.LastInsertId()
			if e != nil {
				return e
			}
			respond(w, 201, map[string]any{"id": id, "name": p.Name})
			return nil
		case "bindings":
			var p struct {
				Topic  int64  `json:"topic_id"`
				Device string `json:"device_id"`
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			if e = s.store.ownedTargets(u.ID, Targets{Devices: []string{p.Device}, Topics: []int64{p.Topic}}); e != nil {
				return e
			}
			if e = s.syncNativeRead(r.Context(), u.ID, p.Device, p.Topic, false); e != nil {
				return e
			}
			_, e = s.store.Exec("INSERT OR IGNORE INTO topic_devices VALUES(?,?)", p.Topic, p.Device)
			if e != nil {
				return e
			}
			respond(w, 200, map[string]bool{"success": true})
			return nil
		case "keys":
			var p struct {
				App     string  `json:"app_id"`
				Kind    string  `json:"kind"`
				Label   string  `json:"label"`
				Targets Targets `json:"targets"`
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			var result map[string]any
			if p.Kind == "ntfy" {
				result, e = s.issueNativeKey(r.Context(), u.ID, p.Label, p.Targets)
			} else {
				result, e = s.store.issueKey(u.ID, p.App, p.Kind, p.Label, p.Targets)
			}
			if e != nil {
				return e
			}
			s.store.audit(u.ID, "key_created", result["id"].(string))
			respond(w, 201, result)
			return nil
		case "pairings":
			code := secret("pair_")
			_, e = s.store.Exec("INSERT INTO pairings(hash,user_id,expires) VALUES(?,?,?)", digest(code), u.ID, time.Now().Add(10*time.Minute).Unix())
			if e != nil {
				return e
			}
			respond(w, 201, map[string]any{"code": code, "expires_in": 600})
			return nil
		case "test":
			var p struct {
				Device string  `json:"device_id"`
				Title  *string `json:"title"`
				Body   *string `json:"body"`
				Message
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			targets := Targets{Devices: p.Devices, Topics: p.TopicIDs}
			if p.Device != "" {
				targets.Devices = append(targets.Devices, p.Device)
			}
			if e = s.store.ownedTargets(u.ID, targets); e != nil {
				return e
			}
			k := Key{ID: "test:" + u.ID, UserID: u.ID, Targets: targets}
			m := &p.Message
			m.Title = "球球推送测试"
			if p.Title != nil {
				m.Title = *p.Title
			}
			m.Body = "通知链路测试，请核对图标、声音和接收时间。"
			if p.Body != nil {
				m.Body = *p.Body
			}
			m.Protocol = "native"
			m.Test = true
			if m.Expires > 0 && m.Expires <= time.Now().Unix() {
				return bad("测试消息已过期")
			}
			if m.Priority == 0 {
				m.Priority = 4
			}
			if e = validateMessage(m); e != nil {
				return e
			}
			if e = s.checkCallback(m.Callback); e != nil {
				return e
			}
			if e = s.checkPolicy(u.ID, m); e != nil {
				return e
			}
			if m.ContentType == "html" {
				m.Body = htmlPolicy.Sanitize(m.Body)
			}
			devices, e := s.store.resolve(k, m)
			if e != nil {
				return e
			}
			m, e = s.store.accept(k, m, devices, "")
			if e != nil {
				return e
			}
			select {
			case s.wake <- struct{}{}:
			default:
			}
			success(w, m)
			return nil
		case "keys/reset":
			var p struct {
				ID string `json:"id"`
			}
			if e = decode(r, &p); e != nil {
				return e
			}
			var kind, label, app, targets string
			if e = s.store.QueryRow("SELECT kind,label,coalesce(app_id,''),targets FROM api_keys WHERE id=? AND user_id=?", p.ID, u.ID).Scan(&kind, &label, &app, &targets); e != nil {
				return forbidden()
			}
			var t Targets
			if e = json.Unmarshal([]byte(targets), &t); e != nil {
				return e
			}
			if kind == "ntfy" {
				return bad("ntfy 凭证请先撤销再重新签发")
			}
			result, e := s.store.issueKey(u.ID, app, kind, label, t)
			if e != nil {
				return e
			}
			if _, e = s.store.Exec("UPDATE api_keys SET disabled=1 WHERE id=? AND user_id=?", p.ID, u.ID); e != nil {
				return e
			}
			respond(w, 201, result)
			return nil
		}
	}
	if method == "PATCH" {
		var p struct {
			ID       string `json:"id"`
			Disabled bool   `json:"disabled"`
		}
		if e = decode(r, &p); e != nil {
			return e
		}
		switch resource {
		case "keys":
			if !p.Disabled {
				return bad("禁用凭证不可恢复，请重置或重新签发")
			}
			if p.Disabled {
				var kind string
				if err := s.store.QueryRow("SELECT kind FROM api_keys WHERE id=? AND user_id=?", p.ID, u.ID).Scan(&kind); err != nil {
					return forbidden()
				}
				if kind == "ntfy" {
					if e = s.ntfyRequest(r.Context(), "DELETE", "/v1/users", map[string]string{"username": "k_" + strings.ReplaceAll(p.ID, "-", "")}, nil, true); e != nil {
						return e
					}
				}
			}
			e = checkRows(s.store.Exec("UPDATE api_keys SET disabled=? WHERE id=? AND user_id=?", p.Disabled, p.ID, u.ID))
		case "devices":
			var d Device
			if e = s.store.QueryRow("SELECT id,user_id,ntfy_user FROM devices WHERE id=? AND user_id=?", p.ID, u.ID).Scan(&d.ID, &d.UserID, &d.NtfyUser); e != nil {
				return forbidden()
			}
			if !p.Disabled {
				return bad("已撤销的设备必须重新配对")
			}
			if e = s.ntfyRequest(r.Context(), "DELETE", "/v1/users", map[string]string{"username": d.NtfyUser}, nil, true); e != nil {
				return e
			}
			e = checkRows(s.store.Exec("UPDATE devices SET disabled=1 WHERE id=?", d.ID))
		case "users":
			if u.Role != "admin" {
				return forbidden()
			}
			if p.ID == u.ID {
				return bad("不能禁用当前管理员")
			}
			if p.Disabled {
				nativeKeys, err := s.store.list("SELECT id FROM api_keys WHERE user_id=? AND kind='ntfy' AND disabled=0", p.ID)
				if err != nil {
					return err
				}
				for _, key := range nativeKeys {
					if err = s.ntfyRequest(r.Context(), "DELETE", "/v1/users", map[string]string{"username": "k_" + strings.ReplaceAll(key["id"].(string), "-", "")}, nil, true); err != nil {
						return err
					}
				}
				devices, e := s.store.list("SELECT ntfy_user FROM devices WHERE user_id=? AND disabled=0", p.ID)
				if e != nil {
					return e
				}
				for _, d := range devices {
					if e = s.ntfyRequest(r.Context(), "DELETE", "/v1/users", map[string]string{"username": d["ntfy_user"].(string)}, nil, true); e != nil {
						return e
					}
				}
				if _, e = s.store.Exec("UPDATE devices SET disabled=1 WHERE user_id=?", p.ID); e != nil {
					return e
				}
			}
			e = checkRows(s.store.Exec("UPDATE users SET disabled=? WHERE id=?", p.Disabled, p.ID))
		default:
			return &AppError{404, "not_found", "接口不存在"}
		}
		if e != nil {
			return e
		}
		s.store.audit(u.ID, resource+"_updated", p.ID)
		respond(w, 200, map[string]bool{"success": true})
		return nil
	}
	if method == "DELETE" && resource == "bindings" {
		var p struct {
			Topic  int64  `json:"topic_id"`
			Device string `json:"device_id"`
		}
		if e = decode(r, &p); e != nil {
			return e
		}
		if e = s.store.ownedTargets(u.ID, Targets{Devices: []string{p.Device}, Topics: []int64{p.Topic}}); e != nil {
			return e
		}
		if e = s.syncNativeRead(r.Context(), u.ID, p.Device, p.Topic, true); e != nil {
			return e
		}
		_, e = s.store.Exec("DELETE FROM topic_devices WHERE topic_id=? AND device_id=?", p.Topic, p.Device)
		if e != nil {
			return e
		}
		respond(w, 200, map[string]bool{"success": true})
		return nil
	}
	return &AppError{405, "method_not_allowed", "不支持此操作"}
}
func (s *Server) listResource(w http.ResponseWriter, r *http.Request, u User, resource string) error {
	limit := 100
	if n, e := strconv.Atoi(r.URL.Query().Get("limit")); e == nil && n > 0 && n <= 500 {
		limit = n
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	var query string
	args := []any{u.ID}
	switch resource {
	case "users":
		if u.Role != "admin" {
			return forbidden()
		}
		query = "SELECT id,name,role,disabled FROM users ORDER BY name"
		args = nil
	case "apps":
		query = "SELECT id,name FROM apps WHERE user_id=? ORDER BY name"
	case "devices":
		query = "SELECT id,name,inbox,disabled FROM devices WHERE user_id=? ORDER BY name"
	case "topics":
		query = "SELECT id,name FROM topics WHERE user_id=? ORDER BY name"
	case "bindings":
		query = "SELECT td.topic_id,td.device_id FROM topic_devices td JOIN topics t ON t.id=td.topic_id WHERE t.user_id=?"
	case "keys":
		query = "SELECT id,kind,label,hint,targets,disabled,created_at,app_id,coalesce((SELECT policy_id FROM key_routes r WHERE r.key_id=api_keys.id),'') delivery_policy,EXISTS(SELECT 1 FROM compat_access_settings a WHERE a.key_id=api_keys.id) access_enabled FROM api_keys WHERE user_id=? ORDER BY created_at DESC"
	case "messages":
		query = "SELECT m.id,m.protocol,m.status,m.created_at,json_extract(m.payload,'$.title') AS title,json_extract(m.payload,'$.body') AS body,(SELECT count(*) FROM deliveries d WHERE d.message_id=m.id) target_count,(SELECT count(*) FROM receipts r WHERE r.message_id=m.id AND r.stage='received') received_count,(SELECT count(*) FROM receipts r WHERE r.message_id=m.id AND r.stage='notification_submitted') notified_count,(SELECT count(*) FROM receipts r WHERE r.message_id=m.id AND r.stage='opened') opened_count FROM messages m WHERE m.user_id=? ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	case "deliveries":
		query = "SELECT d.id,d.message_id,d.device_id,d.status,d.attempts,d.error,d.ntfy_id FROM deliveries d JOIN messages m ON m.id=d.message_id WHERE m.user_id=? ORDER BY d.id DESC LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	case "audit":
		query = "SELECT event,detail,created_at FROM audit WHERE user_id=? ORDER BY id DESC LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	case "stats":
		query = "SELECT a.protocol,sum(a.calls) AS requests,(SELECT count(*) FROM messages m WHERE m.user_id=a.user_id AND m.protocol=a.protocol) AS accepted,(SELECT count(*) FROM messages m WHERE m.user_id=a.user_id AND m.protocol=a.protocol AND status='published') AS published,(SELECT count(*) FROM messages m WHERE m.user_id=a.user_id AND m.protocol=a.protocol AND status='failed') AS failed FROM api_usage a WHERE a.user_id=? GROUP BY a.protocol"
	case "native":
		if u.Role != "admin" {
			return forbidden()
		}
		query = "SELECT ntfy_id,payload,created_at FROM native_publications ORDER BY created_at DESC LIMIT ? OFFSET ?"
		args = []any{limit, offset}
	default:
		return &AppError{404, "not_found", "记录类型不存在"}
	}
	data, e := s.store.list(query, args...)
	if e != nil {
		return e
	}
	respond(w, 200, map[string]any{"data": data})
	return nil
}
func (s *Server) claim(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if e := decode(r, &p); e != nil {
		return e
	}
	if len(p.Name) < 1 || len(p.Name) > 100 {
		return bad("设备名称无效")
	}
	s.provision.Lock()
	var uid string
	e := s.store.QueryRow("SELECT p.user_id FROM pairings p JOIN users u ON u.id=p.user_id WHERE p.hash=? AND p.claimed=0 AND p.expires>? AND u.disabled=0", digest(p.Code), time.Now().Unix()).Scan(&uid)
	if e != nil {
		s.provision.Unlock()
		return &AppError{401, "invalid_pairing", "配对码无效、已使用或已过期"}
	}
	result, e := s.store.Exec("UPDATE pairings SET claimed=1 WHERE hash=? AND claimed=0", digest(p.Code))
	s.provision.Unlock()
	if e = checkRows(result, e); e != nil {
		return e
	}
	data, e := s.provisionDevice(r.Context(), uid, p.Name)
	if e != nil {
		_, _ = s.store.Exec("UPDATE pairings SET claimed=0 WHERE hash=?", digest(p.Code))
		return &AppError{503, "ntfy_unavailable", "设备授权失败，请稍后重试"}
	}
	s.store.audit(uid, "device_paired", data["device_id"].(string))
	respond(w, 201, data)
	return nil
}
