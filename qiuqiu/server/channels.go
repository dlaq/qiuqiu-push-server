package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const channelSchema = `
CREATE TABLE IF NOT EXISTS outbound_channels(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,name TEXT NOT NULL,kind TEXT NOT NULL,config TEXT NOT NULL,disabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS key_encryption(key_id TEXT PRIMARY KEY REFERENCES api_keys(id),config TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS key_routes(key_id TEXT PRIMARY KEY REFERENCES api_keys(id),policy_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS transport_ids(delivery_id INTEGER NOT NULL REFERENCES deliveries(id),channel TEXT NOT NULL,transport_id TEXT NOT NULL,created_at INTEGER NOT NULL,PRIMARY KEY(delivery_id,channel,transport_id));
CREATE TABLE IF NOT EXISTS icon_assets(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,content BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS delivery_policies(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,name TEXT NOT NULL,mode TEXT NOT NULL,steps TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS route_attempts(id INTEGER PRIMARY KEY AUTOINCREMENT,delivery_id INTEGER NOT NULL REFERENCES deliveries(id),channel_id TEXT NOT NULL,step INTEGER NOT NULL,state TEXT NOT NULL,started_at INTEGER NOT NULL,deadline INTEGER NOT NULL,detail TEXT NOT NULL DEFAULT '',UNIQUE(delivery_id,step));
CREATE TRIGGER IF NOT EXISTS route_insert_event AFTER INSERT ON route_attempts BEGIN
 INSERT INTO delivery_events(user_id,message_id,device_id,channel,stage,created_at,detail) SELECT m.user_id,v.message_id,coalesce(v.device_id,''),NEW.channel_id,NEW.state,unixepoch(),NEW.detail FROM deliveries v JOIN messages m ON m.id=v.message_id WHERE v.id=NEW.delivery_id; END;
CREATE TRIGGER IF NOT EXISTS route_update_event AFTER UPDATE OF state ON route_attempts WHEN NEW.state<>OLD.state BEGIN
 INSERT INTO delivery_events(user_id,message_id,device_id,channel,stage,created_at,detail) SELECT m.user_id,v.message_id,coalesce(v.device_id,''),NEW.channel_id,NEW.state,unixepoch(),NEW.detail FROM deliveries v JOIN messages m ON m.id=v.message_id WHERE v.id=NEW.delivery_id; END;
CREATE TRIGGER IF NOT EXISTS default_receipt_wait AFTER UPDATE OF status ON deliveries WHEN NEW.status='published' AND OLD.status<>'published' AND coalesce((SELECT json_extract(payload,'$.delivery_policy') FROM messages WHERE id=NEW.message_id),'')='' BEGIN
 INSERT OR IGNORE INTO route_attempts(delivery_id,channel_id,step,state,started_at,deadline,detail) VALUES(NEW.id,CASE WHEN NEW.ntfy_id LIKE 'direct_%' THEN 'app_direct' ELSE 'ntfy_fcm' END,-2,'waiting_receipt',unixepoch(),unixepoch()+60,'等待设备接收回执'); END;
CREATE TRIGGER IF NOT EXISTS native_receipt_wait AFTER INSERT ON deliveries WHEN NEW.status='published' AND NEW.device_id IS NOT NULL AND coalesce((SELECT json_extract(payload,'$.delivery_policy') FROM messages WHERE id=NEW.message_id),'')='' BEGIN
 INSERT OR IGNORE INTO route_attempts(delivery_id,channel_id,step,state,started_at,deadline,detail) VALUES(NEW.id,'ntfy_fcm',-2,'waiting_receipt',unixepoch(),unixepoch()+60,'ntfy已受理，等待设备接收回执'); END;
INSERT OR IGNORE INTO schema_version VALUES(3);
`

type ChannelConfig map[string]string
type outboundPrivateKey struct{}
type PolicyStep struct {
	Channel string `json:"channel_id"`
	Timeout int    `json:"timeout_seconds"`
}
type DeliveryPolicy struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Mode  string       `json:"mode"`
	Steps []PolicyStep `json:"steps"`
}

var channelNames = map[string]string{"ntfy_fcm": "ntfy → FCM", "app_direct": "APP 直连", "email": "SMTP 邮件", "wechat_test": "微信测试号", "wecom_app": "企业微信应用", "wecom_bot": "企业微信群机器人", "feishu_app": "飞书应用", "feishu_bot": "飞书群机器人", "dingtalk": "钉钉群机器人", "onebot": "QQ OneBot", "qq_bot": "QQ 官方机器人", "telegram": "Telegram", "discord": "Discord", "bark": "Bark", "webhook": "自定义 Webhook", "store": "仅保存"}

func (s *Server) initChannels() error {
	if _, e := s.store.Exec(channelSchema); e != nil {
		return e
	}
	if s.cfg.Database == ":memory:" {
		s.channelKey = make([]byte, 32)
		_, e := rand.Read(s.channelKey)
		return e
	}
	path := filepath.Join(filepath.Dir(s.cfg.Database), "channel.key")
	key, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		key = make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return e
		}
		var f *os.File
		f, e = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(key)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	} else if e != nil {
		return e
	}
	if len(key) != 32 {
		return fmt.Errorf("channel.key must contain 32 bytes")
	}
	s.channelKey = key
	return nil
}
func (s *Server) sealConfig(c ChannelConfig) (string, error) {
	if c == nil {
		c = ChannelConfig{}
	}
	block, e := aes.NewCipher(s.channelKey)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(encode(c)), []byte("qiuqiu.channels.v1"))), nil
}
func (s *Server) readChannel(uid, id string) (string, ChannelConfig, error) {
	var kind, encrypted string
	e := s.store.QueryRow("SELECT c.kind,c.config FROM outbound_channels c JOIN users u ON u.id=c.user_id WHERE c.id=? AND c.user_id=? AND c.disabled=0 AND u.disabled=0", id, uid).Scan(&kind, &encrypted)
	if e != nil {
		return "", nil, forbidden()
	}
	raw, e := base64.StdEncoding.DecodeString(encrypted)
	if e != nil {
		return "", nil, e
	}
	block, _ := aes.NewCipher(s.channelKey)
	g, _ := cipher.NewGCM(block)
	if len(raw) < g.NonceSize() {
		return "", nil, bad("通道配置损坏")
	}
	plain, e := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], []byte("qiuqiu.channels.v1"))
	if e != nil {
		return "", nil, e
	}
	c := ChannelConfig{}
	e = json.Unmarshal(plain, &c)
	if e != nil {
		return "", nil, e
	}
	if c == nil {
		c = ChannelConfig{}
	}
	c["_id"] = id
	c["_user"] = uid
	return kind, c, e
}

func (s *Server) channelManage(w http.ResponseWriter, r *http.Request, u User, resource string) (bool, error) {
	if resource == "keys/route" && r.Method == "POST" {
		var p struct {
			ID     string `json:"id"`
			Policy string `json:"delivery_policy"`
		}
		if e := decode(r, &p); e != nil {
			return true, e
		}
		var n int
		if e := s.store.QueryRow("SELECT count(*) FROM api_keys WHERE id=? AND user_id=? AND disabled=0 AND kind<>'ntfy'", p.ID, u.ID).Scan(&n); e != nil || n != 1 {
			return true, forbidden()
		}
		if e := s.checkPolicy(u.ID, &Message{Policy: p.Policy}); e != nil {
			return true, e
		}
		_, e := s.store.Exec("INSERT INTO key_routes VALUES(?,?) ON CONFLICT(key_id) DO UPDATE SET policy_id=excluded.policy_id", p.ID, p.Policy)
		if e == nil {
			respond(w, 200, params{"success": true})
		}
		return true, e
	}
	if resource != "channels" && resource != "policies" && resource != "capabilities" {
		return false, nil
	}
	if r.Method == "GET" {
		if resource == "capabilities" {
			respond(w, 200, params{"channels": channelNames, "sounds": sounds, "businesses": businesses, "receipt_timeout_seconds": 60})
			return true, nil
		}
		table := "outbound_channels"
		fields := "id,name,kind,disabled"
		if resource == "policies" {
			table = "delivery_policies"
			fields = "id,name,mode,steps"
		}
		rows, e := s.store.list("SELECT "+fields+" FROM "+table+" WHERE user_id=?", u.ID)
		if e == nil {
			respond(w, 200, params{"data": rows})
		}
		return true, e
	}
	if r.Method == "DELETE" {
		id := r.URL.Query().Get("id")
		table := "delivery_policies"
		if resource == "channels" {
			table = "outbound_channels"
			e := checkRows(s.store.Exec("UPDATE "+table+" SET disabled=1 WHERE id=? AND user_id=?", id, u.ID))
			if e == nil {
				respond(w, 200, params{"success": true})
			}
			return true, e
		}
		e := checkRows(s.store.Exec("DELETE FROM "+table+" WHERE id=? AND user_id=?", id, u.ID))
		if e == nil {
			respond(w, 200, params{"success": true})
		}
		return true, e
	}
	if resource == "capabilities" || (r.Method != "POST" && r.Method != "PATCH") {
		return true, &AppError{405, "method_not_allowed", "不支持此操作"}
	}
	if resource == "channels" {
		var p struct {
			ID     string        `json:"id"`
			Name   string        `json:"name"`
			Kind   string        `json:"kind"`
			Config ChannelConfig `json:"config"`
		}
		if e := decode(r, &p); e != nil {
			return true, e
		}
		if p.Config["allow_private"] == "true" && u.Role != "admin" {
			return true, forbidden()
		}
		if strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 || channelNames[p.Kind] == "" {
			return true, bad("通道名称或类型无效")
		}
		if p.Kind == "tencent_alarm" {
			return true, bad("腾讯云已于2026-09删除SendCustomAlarmMsg接口，当前不可配置")
		}
		if p.Kind == "client" && len(p.Config["secret"]) < 24 {
			return true, bad("客户端连接密钥至少24字节")
		}
		if e := validateChannel(p.Kind, p.Config); e != nil {
			return true, e
		}
		encrypted, e := s.sealConfig(p.Config)
		if e != nil {
			return true, e
		}
		id, status := uuid(), 201
		if r.Method == "PATCH" {
			id, status = p.ID, 200
			e = checkRows(s.store.Exec("UPDATE outbound_channels SET name=?,config=? WHERE id=? AND user_id=? AND kind=? AND disabled=0", p.Name, encrypted, id, u.ID, p.Kind))
		} else {
			_, e = s.store.Exec("INSERT INTO outbound_channels VALUES(?,?,?,?,?,0)", id, u.ID, p.Name, p.Kind, encrypted)
		}
		if e == nil {
			respond(w, status, params{"id": id, "name": p.Name, "kind": p.Kind})
		}
		return true, e
	}
	var p DeliveryPolicy
	if e := decode(r, &p); e != nil {
		return true, e
	}
	if len(p.Steps) == 0 || len(p.Steps) > 16 || strings.TrimSpace(p.Name) == "" || (p.Mode != "fallback" && p.Mode != "parallel" && p.Mode != "single") {
		return true, bad("策略需要名称、single/parallel/fallback模式和1–16个通道")
	}
	if p.Mode == "single" && len(p.Steps) != 1 {
		return true, bad("单通道策略只能选择一个通道")
	}
	seen := map[string]bool{}
	for i := range p.Steps {
		step := &p.Steps[i]
		if seen[step.Channel] {
			return true, bad("策略不能重复包含同一通道")
		}
		seen[step.Channel] = true
		if _, _, e := s.readChannel(u.ID, step.Channel); e != nil {
			return true, e
		}
		if step.Timeout == 0 {
			step.Timeout = 60
		}
		if step.Timeout < 5 || step.Timeout > 3600 {
			return true, bad("回执超时范围为5–3600秒")
		}
	}
	status := 201
	var e error
	if r.Method == "PATCH" {
		status = 200
		e = checkRows(s.store.Exec("UPDATE delivery_policies SET name=?,mode=?,steps=? WHERE id=? AND user_id=?", p.Name, p.Mode, encode(p.Steps), p.ID, u.ID))
	} else {
		p.ID = uuid()
		_, e = s.store.Exec("INSERT INTO delivery_policies VALUES(?,?,?,?,?)", p.ID, u.ID, p.Name, p.Mode, encode(p.Steps))
	}
	if e == nil {
		respond(w, status, p)
	}
	return true, e
}

func validateChannel(kind string, c ChannelConfig) error {
	required := map[string][]string{
		"email":       {"host", "username", "password", "recipient"},
		"wechat_test": {"app_id", "secret", "template_id", "recipient"},
		"wecom_app":   {"app_id", "secret", "agent_id", "recipient"},
		"wecom_bot":   {"url"}, "feishu_app": {"app_id", "secret", "recipient"},
		"feishu_bot": {"url"}, "dingtalk": {"url"}, "onebot": {"recipient"},
		"qq_bot": {"app_id", "secret", "recipient"}, "telegram": {"token", "recipient"},
		"discord": {"url"}, "bark": {"url", "key"}, "webhook": {"url"}, "client": {"secret"},
	}
	for _, field := range required[kind] {
		if strings.TrimSpace(c[field]) == "" {
			return bad("缺少通道配置：" + field)
		}
	}
	if kind == "onebot" && c["mode"] != "reverse" && c["url"] == "" {
		return bad("OneBot正向连接需要url")
	}
	if raw := c["url"]; raw != "" {
		u, e := url.Parse(raw)
		if e != nil || u.Hostname() == "" || u.User != nil {
			return bad("通道地址无效")
		}
		if u.Scheme != "https" && !(kind == "onebot" && (u.Scheme == "wss" || c["allow_private"] == "true" && (u.Scheme == "http" || u.Scheme == "ws"))) {
			return bad("通道地址需要HTTPS（OneBot可使用WSS）")
		}
	}
	if kind == "email" && c["port"] != "" && c["port"] != "465" && c["port"] != "587" {
		return bad("SMTP只支持465 TLS或587 STARTTLS")
	}
	for _, field := range []string{"headers", "body_template"} {
		if c[field] != "" {
			var value map[string]any
			if json.Unmarshal([]byte(c[field]), &value) != nil || value == nil {
				return bad(field + "必须是JSON对象")
			}
		}
	}
	return nil
}
func (s *Server) checkPolicy(uid string, m *Message) error {
	m.PolicySnapshot = nil
	if m.Policy == "" {
		return nil
	}
	var p DeliveryPolicy
	var raw string
	p.ID = m.Policy
	if e := s.store.QueryRow("SELECT name,mode,steps FROM delivery_policies WHERE id=? AND user_id=?", m.Policy, uid).Scan(&p.Name, &p.Mode, &raw); e != nil {
		return forbidden()
	}
	if e := json.Unmarshal([]byte(raw), &p.Steps); e != nil {
		return e
	}
	m.PolicySnapshot = &p
	return nil
}

func (s *Server) processRoute(ctx context.Context, delivery int64, device, topic string, m Message) (string, error) {
	var mode, raw string
	var steps []PolicyStep
	if m.PolicySnapshot != nil {
		mode = m.PolicySnapshot.Mode
		steps = m.PolicySnapshot.Steps
	} else {
		if e := s.store.QueryRow("SELECT mode,steps FROM delivery_policies WHERE id=? AND user_id=?", m.Policy, m.UserID).Scan(&mode, &raw); e != nil {
			return "failed", nil
		}
		if e := json.Unmarshal([]byte(raw), &steps); e != nil {
			return "failed", e
		}
	}
	success, pending := false, false
	for i, step := range steps {
		var state string
		var deadline int64
		e := s.store.QueryRow("SELECT state,deadline FROM route_attempts WHERE delivery_id=? AND step=?", delivery, i).Scan(&state, &deadline)
		if e == nil && state == "waiting_receipt" {
			var n int
			_ = s.store.QueryRow("SELECT count(*) FROM receipts WHERE message_id=? AND device_id=? AND stage='received'", m.ID, device).Scan(&n)
			if n > 0 {
				state = "received"
			} else if time.Now().Unix() >= deadline {
				state = "receipt_timeout"
			} else {
				pending = true
				if mode != "parallel" {
					return "pending", nil
				}
				continue
			}
			if _, e = s.store.Exec("UPDATE route_attempts SET state=? WHERE delivery_id=? AND step=?", state, delivery, i); e != nil {
				return "pending", e
			}
		}
		if state == "provider_accepted" || state == "received" || state == "stored" {
			success = true
			if mode != "parallel" {
				return "published", nil
			}
			continue
		}
		if state == "failed" || state == "receipt_timeout" {
			continue
		}
		if state == "sending" { // A crash during HTTP leaves an uncertain outcome; do not duplicate blindly.
			_, e = s.store.Exec("UPDATE route_attempts SET state='failed',detail='服务中断，通道受理结果未知' WHERE delivery_id=? AND step=?", delivery, i)
			if e != nil {
				return "pending", e
			}
			continue
		}
		if _, e = s.store.Exec("INSERT INTO route_attempts(delivery_id,channel_id,step,state,started_at,deadline) VALUES(?,?,?,'sending',?,?)", delivery, step.Channel, i, time.Now().Unix(), time.Now().Unix()+int64(step.Timeout)); e != nil {
			return "pending", e
		}
		kind, c, err := s.readChannel(m.UserID, step.Channel)
		needsReceipt := kind == "ntfy_fcm" || kind == "app_direct"
		alreadySent := false
		if err == nil {
			if !needsReceipt && kind != "store" {
				var already int
				_ = s.store.QueryRow("SELECT count(*) FROM route_attempts a JOIN deliveries v ON v.id=a.delivery_id WHERE v.message_id=? AND a.channel_id=? AND a.state='provider_accepted'", m.ID, step.Channel).Scan(&already)
				alreadySent = already > 0
			}
			switch kind {
			case "ntfy_fcm":
				var transportID string
				transportID, err = s.publish(ctx, m, topic)
				if err == nil {
					_, err = s.store.Exec("INSERT OR IGNORE INTO transport_ids VALUES(?,?,?,?)", delivery, kind, transportID, time.Now().Unix())
				}
			case "app_direct":
				err = s.sendDirect(ctx, device, m, topic)
				if err == nil {
					_, err = s.store.Exec("INSERT OR IGNORE INTO transport_ids VALUES(?,?,?,?)", delivery, kind, "direct_"+m.ID, time.Now().Unix())
				}
			case "store":
			default:
				if !alreadySent {
					err = s.sendExternal(ctx, kind, c, m)
				}
			}
		}
		state = "provider_accepted"
		detail := "提供方已受理；终端接收未知"
		if alreadySent {
			detail = "同一消息已投递到此通道，复用受理结果"
		}
		if err != nil {
			state = "failed"
			detail = "通道未受理，请检查配置、权限或网络"
		} else if needsReceipt {
			state = "waiting_receipt"
			detail = "等待设备回执"
			pending = true
		} else if kind == "store" {
			state = "stored"
			detail = "仅保存，未发送"
		}
		if _, e = s.store.Exec("UPDATE route_attempts SET state=?,detail=? WHERE delivery_id=? AND step=?", state, detail, delivery, i); e != nil {
			return "pending", e
		}
		if err == nil {
			if !needsReceipt {
				success = true
			}
			if mode != "parallel" {
				if needsReceipt {
					return "pending", nil
				}
				return "published", nil
			}
		}
	}
	if pending {
		return "pending", nil
	}
	if success {
		return "published", nil
	}
	return "failed", nil
}

// All external requests are bounded, reject redirects and redact remote error bodies.
func (s *Server) externalJSON(ctx context.Context, method, endpoint string, body any, headers map[string]string) (params, error) {
	data := []byte(nil)
	if body != nil {
		data = []byte(encode(body))
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := *s.client
	if client.Transport == nil {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			allowPrivate, _ := ctx.Value(outboundPrivateKey{}).(bool)
			for _, ip := range ips {
				if !allowPrivate && (!ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast()) {
					return nil, fmt.Errorf("通道不允许访问私网地址")
				}
			}
			for _, ip := range ips {
				conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("通道连接失败")
		}}
		defer client.Transport.(*http.Transport).CloseIdleConnections()
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return nil, fmt.Errorf("通道请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("通道 HTTP %d", resp.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("通道响应过大")
	}
	return decodeProviderJSON(raw)
}

func decodeProviderJSON(raw []byte) (params, error) {
	p := params{}
	if len(raw) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if e := decoder.Decode(&p); e != nil {
			return nil, fmt.Errorf("通道响应不是 JSON")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return nil, fmt.Errorf("通道响应不是单个JSON对象")
		}
	}
	return p, nil
}
func (s *Server) sendExternal(ctx context.Context, kind string, c ChannelConfig, m Message) error {
	if c["allow_private"] == "true" {
		ctx = context.WithValue(ctx, outboundPrivateKey{}, true)
	}
	text := m.Body
	if m.Title != "" {
		text = m.Title + "\n" + text
	}
	if m.Click != "" {
		text += "\n" + m.Click
	}
	headers := map[string]string{}
	endpoint := c["url"]
	var body any
	check := ""
	switch kind {
	case "client":
		return s.sendLegacyClient(ctx, c, m)
	case "wecom_bot":
		body = params{"msgtype": "text", "text": params{"content": text}}
		check = "errcode"
	case "feishu_bot":
		body = params{"msg_type": "text", "content": params{"text": text}}
		check = "code"
		if c["secret"] != "" {
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(timestamp+"\n"+c["secret"]))
			body.(params)["timestamp"] = timestamp
			body.(params)["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	case "dingtalk":
		body = params{"msgtype": "text", "text": params{"content": text}}
		check = "errcode"
		if c["secret"] != "" {
			u, e := url.Parse(endpoint)
			if e != nil {
				return e
			}
			timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(c["secret"]))
			mac.Write([]byte(timestamp + "\n" + c["secret"]))
			q := u.Query()
			q.Set("timestamp", timestamp)
			q.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery = q.Encode()
			endpoint = u.String()
		}
	case "telegram":
		endpoint = "https://api.telegram.org/bot" + c["token"] + "/sendMessage"
		body = params{"chat_id": c["recipient"], "text": text, "disable_web_page_preview": true}
		check = "ok"
	case "discord":
		body = params{"content": text, "allowed_mentions": params{"parse": []string{}}}
	case "bark":
		body = params{"device_key": c["key"], "title": m.Title, "body": m.Body, "url": m.Click, "sound": m.Sound, "group": m.Group}
		check = "code"
		if endpoint == "" {
			endpoint = "https://api.day.app/push"
		}
	case "onebot":
		body = params{"message": text, "auto_escape": true}
		if c["recipient_type"] == "group" {
			id, e := strconv.ParseInt(c["recipient"], 10, 64)
			if e != nil {
				return e
			}
			body.(params)["group_id"] = id
			body.(params)["message_type"] = "group"
		} else {
			id, e := strconv.ParseInt(c["recipient"], 10, 64)
			if e != nil {
				return e
			}
			body.(params)["user_id"] = id
			body.(params)["message_type"] = "private"
		}
		if c["token"] != "" {
			headers["Authorization"] = "Bearer " + c["token"]
		}
		check = "retcode"
		if c["mode"] == "reverse" {
			return s.sendOneBotReverse(ctx, c, body)
		}
		if strings.HasPrefix(endpoint, "ws://") || strings.HasPrefix(endpoint, "wss://") {
			return sendOneBotWS(ctx, c, body)
		}
	case "wecom_app":
		token, e := s.externalJSON(ctx, "GET", "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid="+url.QueryEscape(c["app_id"])+"&corpsecret="+url.QueryEscape(c["secret"]), nil, nil)
		if e != nil {
			return e
		}
		if token.str("access_token") == "" {
			return fmt.Errorf("企业微信授权失败")
		}
		endpoint = "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + url.QueryEscape(token.str("access_token"))
		agent, e := strconv.Atoi(c["agent_id"])
		if e != nil {
			return e
		}
		body = params{"touser": c["recipient"], "agentid": agent, "msgtype": "text", "text": params{"content": text}}
		check = "errcode"
	case "wechat_test":
		token, e := s.externalJSON(ctx, "GET", "https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid="+url.QueryEscape(c["app_id"])+"&secret="+url.QueryEscape(c["secret"]), nil, nil)
		if e != nil {
			return e
		}
		if token.str("access_token") == "" {
			return fmt.Errorf("微信授权失败")
		}
		endpoint = "https://api.weixin.qq.com/cgi-bin/message/template/send?access_token=" + url.QueryEscape(token.str("access_token"))
		body = params{"touser": c["recipient"], "template_id": c["template_id"], "url": m.Click, "data": params{"title": params{"value": m.Title}, "description": params{"value": m.Body}, "content": params{"value": m.Body}, "text": params{"value": text}}}
		check = "errcode"
	case "feishu_app":
		token, e := s.externalJSON(ctx, "POST", "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal", params{"app_id": c["app_id"], "app_secret": c["secret"]}, nil)
		if e != nil {
			return e
		}
		if token.str("tenant_access_token") == "" {
			return fmt.Errorf("飞书授权失败")
		}
		headers["Authorization"] = "Bearer " + token.str("tenant_access_token")
		endpoint = "https://open.feishu.cn/open-apis/im/v1/messages?receive_id_type=" + url.QueryEscape(first(c["recipient_type"], "open_id"))
		body = params{"receive_id": c["recipient"], "msg_type": "text", "content": encode(params{"text": text}), "uuid": m.ID}
		check = "code"
	case "qq_bot":
		token, e := s.externalJSON(ctx, "POST", "https://bots.qq.com/app/getAppAccessToken", params{"appId": c["app_id"], "clientSecret": c["secret"]}, nil)
		if e != nil {
			return e
		}
		if token.str("access_token") == "" {
			return fmt.Errorf("QQ机器人授权失败")
		}
		headers["Authorization"] = "QQBot " + token.str("access_token")
		kind := "users"
		if c["recipient_type"] == "group" {
			kind = "groups"
		}
		endpoint = "https://api.sgroup.qq.com/v2/" + kind + "/" + url.PathEscape(c["recipient"]) + "/messages"
		body = params{"content": text, "msg_type": 0}
	case "email":
		return sendEmail(ctx, c, m)
	case "webhook":
		u, e := url.Parse(endpoint)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return bad("Webhook 必须是 HTTPS URL")
		}
		ips, e := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
		if e != nil {
			return e
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() {
				return bad("Webhook 不允许私网地址")
			}
		}
		body = params{"message_id": m.ID, "title": m.Title, "body": m.Body, "click_url": m.Click, "business_type": m.Business}
		if c["body_template"] != "" {
			var template any
			if json.Unmarshal([]byte(c["body_template"]), &template) != nil {
				return bad("Webhook模板须为JSON")
			}
			body = renderWebhook(template, m)
		}
		if c["headers"] != "" {
			if json.Unmarshal([]byte(c["headers"]), &headers) != nil {
				return bad("Webhook请求头须为JSON字符串对象")
			}
			for k := range headers {
				if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Connection") {
					return bad("禁止的Webhook请求头")
				}
			}
		}
		if c["token"] != "" {
			headers["Authorization"] = "Bearer " + c["token"]
		}
	default:
		return bad("通道尚未实现")
	}
	if endpoint == "" {
		return bad("通道缺少 URL")
	}
	p, e := s.externalJSON(ctx, "POST", endpoint, body, headers)
	if e != nil {
		return e
	}
	switch check {
	case "ok":
		if p["ok"] != true {
			return fmt.Errorf("提供方拒绝")
		}
	case "code":
		wanted := int64(0)
		if kind == "bark" {
			wanted = 200
		}
		if !providerCode(p, "code", wanted) {
			return fmt.Errorf("提供方拒绝")
		}
	case "errcode", "retcode":
		if !providerCode(p, check, 0) || kind == "onebot" && p.str("status") != "ok" {
			return fmt.Errorf("提供方拒绝")
		}
	}
	if kind == "qq_bot" && p.str("id") == "" {
		return fmt.Errorf("QQ未返回消息ID")
	}
	return nil
}

// A missing, null or malformed code must never be coerced into success (zero).
func providerCode(p params, field string, wanted int64) bool {
	code, err := strconv.ParseInt(p.str(field), 10, 64)
	return err == nil && code == wanted
}

func renderWebhook(value any, m Message) any {
	switch v := value.(type) {
	case string:
		for token, replacement := range map[string]string{"{{message_id}}": m.ID, "{{title}}": m.Title, "{{body}}": m.Body, "{{click_url}}": m.Click, "{{business_type}}": m.Business} {
			v = strings.ReplaceAll(v, token, replacement)
		}
		return v
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			out[k] = renderWebhook(item, m)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = renderWebhook(item, m)
		}
		return out
	default:
		return value
	}
}

func sendEmail(ctx context.Context, c ChannelConfig, m Message) error {
	from, e := mail.ParseAddress(c["username"])
	if e != nil {
		return bad("SMTP 发件人无效")
	}
	to, e := mail.ParseAddress(c["recipient"])
	if e != nil {
		return bad("SMTP 收件人无效")
	}
	host := c["host"]
	port := first(c["port"], "587")
	conn, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if e != nil {
		return fmt.Errorf("SMTP连接失败")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(25 * time.Second))
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if port == "465" {
		secure := tls.Client(conn, tlsConfig)
		if e = secure.HandshakeContext(ctx); e != nil {
			return e
		}
		conn = secure
	}
	client, e := smtp.NewClient(conn, host)
	if e != nil {
		return e
	}
	defer client.Close()
	if port != "465" {
		if e = client.StartTLS(tlsConfig); e != nil {
			return e
		}
	}
	if e = client.Auth(smtp.PlainAuth("", c["username"], c["password"], host)); e != nil {
		return fmt.Errorf("SMTP认证失败")
	}
	if e = client.Mail(from.Address); e != nil {
		return e
	}
	if e = client.Rcpt(to.Address); e != nil {
		return e
	}
	writer, e := client.Data()
	if e != nil {
		return e
	}
	title := strings.ReplaceAll(strings.ReplaceAll(m.Title, "\r", ""), "\n", "")
	_, e = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from.Address, to.Address, base64.StdEncoding.EncodeToString([]byte(title)), m.Body)
	if e != nil {
		return e
	}
	return writer.Close()
}
