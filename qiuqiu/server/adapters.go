package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type params map[string]any

func (p params) str(k string) string {
	v := p[k]
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	case nil:
		return ""
	default:
		return encode(v)
	}
}
func (p params) num(k string) int { i, _ := strconv.Atoi(p.str(k)); return i }
func (p params) list(k string) []string {
	v := p[k]
	if v == nil {
		return nil
	}
	if a, ok := v.([]any); ok {
		out := []string{}
		for _, v := range a {
			out = append(out, params{"x": v}.str("x"))
		}
		return out
	}
	if a, ok := v.([]string); ok {
		return a
	}
	s := p.str(k)
	if strings.HasPrefix(s, "[") {
		var a []any
		if json.Unmarshal([]byte(s), &a) == nil {
			return params{k: a}.list(k)
		}
	}
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '|' })
}
func parseParams(r *http.Request) (params, error) {
	p := params{}
	for k, v := range r.URL.Query() {
		if len(v) == 1 {
			p[k] = v[0]
		} else {
			p[k] = v
		}
	}
	if r.Method == "GET" {
		return p, nil
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return nil, &AppError{413, "payload_too_large", "请求过大"}
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var body params
		if e := d.Decode(&body); e != nil {
			return nil, bad("JSON 请求格式无效")
		}
		var rest any
		if d.Decode(&rest) != io.EOF {
			return nil, bad("仅允许一个 JSON 对象")
		}
		for k, v := range body {
			p[k] = v
		}
	} else if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		b, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if e != nil || len(b) > 1<<20 {
			return nil, bad("请求过大")
		}
		v, e := url.ParseQuery(string(b))
		if e != nil {
			return nil, bad("表单编码无效")
		}
		for k, v := range v {
			if len(v) == 1 {
				p[k] = v[0]
			} else {
				p[k] = v
			}
		}
	} else {
		return nil, &AppError{415, "unsupported_media_type", "使用 JSON 或 FORM 请求"}
	}
	return p, nil
}
func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
func pathParts(r *http.Request) []string {
	a := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	for i, s := range a {
		a[i], _ = url.PathUnescape(s)
	}
	return a
}

func adapt(r *http.Request) (*Message, string, string, error) {
	if r.Method != "GET" && r.Method != "POST" {
		return nil, "", "", &AppError{405, "method_not_allowed", "仅支持 GET/POST"}
	}
	p, e := parseParams(r)
	if e != nil {
		return nil, "", "", e
	}
	for _, field := range []string{"priority", "contentType", "timestamp", "badge", "volume"} {
		if value := p.str(field); value != "" {
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return nil, "", "", bad(field + " 必须是整数")
			}
		}
	}
	a := pathParts(r)
	path := r.URL.Path
	m := &Message{Priority: 3}
	token, kind := "", ""
	switch {
	case path == "/api/v1/push":
		m.Protocol = "native"
		kind = "native"
		token = p.str("token")
		m.Title = p.str("title")
		m.Body = p.str("body")
		m.Topic = p.str("topic")
		m.Devices = p.list("device_ids")
		m.Business = p.str("business_type")
		m.Icon = p.str("icon")
		m.Sound = p.str("sound")
		m.Click = first(p.str("click"), p.str("click_url"))
		m.Priority = p.num("priority")
		m.ContentType = p.str("content_type")
		m.Tags = p.list("tags")
	case len(a) == 2 && a[0] == "sctapi" && strings.HasSuffix(a[1], ".send"):
		m.Protocol = "serverchan"
		kind = "serverchan"
		token = strings.TrimSuffix(a[1], ".send")
	case len(a) == 4 && a[0] == "sc3" && a[2] == "send" && strings.HasSuffix(a[3], ".send"):
		m.Protocol = "sc3"
		kind = "sc3"
		token = strings.TrimSuffix(a[3], ".send")
		p["_sc3_uid"] = a[1]
	case path == "/pushbear/sub":
		m.Protocol = "pushbear"
		kind = "serverchan"
		token = p.str("sendkey")
	case path == "/send" || len(a) == 2 && a[0] == "send":
		m.Protocol = "pushplus"
		kind = "pushplus"
		token = p.str("token")
		if len(a) == 2 {
			token = a[1]
		}
		m.Title = p.str("title")
		m.Body = p.str("content")
		m.Topic = p.str("topic")
		m.ContentType = first(p.str("template"), "html")
		if m.ContentType == "txt" {
			m.ContentType = "text"
		}
		m.UIDs = p.list("to")
		m.Click = first(p.str("url"), p.str("click"))
		m.Callback = p.str("callbackUrl")
		m.Expires = int64(p.num("timestamp")) / 1000
		for _, k := range []string{"channel", "option", "pre"} {
			if p.str(k) != "" {
				m.Warnings = append(m.Warnings, k+" 已接收；不调用外部渠道或执行原平台预处理")
			}
		}
	case path == "/push" || len(a) >= 2 && a[0] == "bark":
		m.Protocol = "bark"
		kind = "bark"
		token = p.str("device_key")
		if len(a) >= 2 && a[0] == "bark" {
			token = a[1]
			parts := a[2:]
			switch len(parts) {
			case 0:
			case 1:
				p["body"] = parts[0]
			case 2:
				p["title"] = parts[0]
				p["body"] = parts[1]
			case 3:
				p["title"] = parts[0]
				p["subtitle"] = parts[1]
				p["body"] = parts[2]
			default:
				return nil, "", "", bad("Bark 路径段数无效")
			}
		}
		m.Title = p.str("title")
		m.Body = p.str("body")
		if sub := p.str("subtitle"); sub != "" {
			m.Body = sub + "\n" + m.Body
		}
		m.Group = p.str("group")
		if businesses[m.Group] {
			m.Business = m.Group
		}
		m.Sound = p.str("sound")
		m.Icon = p.str("icon")
		m.Click = p.str("url")
		switch p.str("level") {
		case "passive":
			m.Priority = 2
		case "timeSensitive", "critical":
			m.Priority = 5
		}
		for _, k := range []string{"badge", "volume", "call"} {
			if p.str(k) != "" {
				m.Warnings = append(m.Warnings, k+" 受 Android 系统限制，不保证实现 iOS 行为")
			}
		}
	case path == "/api/send/message":
		m.Protocol = "wxpusher"
		kind = "wxpusher"
		token = p.str("appToken")
		m.Title = p.str("summary")
		m.Body = p.str("content")
		m.ContentType = map[int]string{0: "text", 1: "text", 2: "html", 3: "markdown"}[p.num("contentType")]
		if m.ContentType == "" {
			return nil, "", "", bad("contentType 必须为 1/2/3")
		}
		m.Click = p.str("url")
		m.UIDs = p.list("uids")
		if u := p.str("uid"); u != "" {
			m.UIDs = append(m.UIDs, u)
		}
		for _, s := range p.list("topicIds") {
			id, e := strconv.ParseInt(s, 10, 64)
			if e != nil || id <= 0 {
				return nil, "", "", bad("TopicId 无效")
			}
			m.TopicIDs = append(m.TopicIDs, id)
		}
		if len(m.UIDs) == 0 && len(m.TopicIDs) == 0 {
			return nil, "", "", bad("必须指定 uids 或 topicIds")
		}
		if p.num("verifyPayType") != 0 {
			m.Warnings = append(m.Warnings, "verifyPayType 不执行付费校验")
		}
	default:
		return nil, "", "", &AppError{404, "not_found", "接口不存在"}
	}
	if m.Protocol == "serverchan" || m.Protocol == "sc3" || m.Protocol == "pushbear" {
		m.Title = first(p.str("title"), p.str("text"))
		m.Body = first(p.str("desp"), p.str("short"), m.Title)
		m.ContentType = "markdown"
		m.Tags = p.list("tags")
		m.Click = first(p.str("url"), p.str("click"))
	}
	if h := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); h != "" && h != r.Header.Get("Authorization") {
		if token != "" && token != h {
			return nil, "", "", bad("认证信息冲突")
		}
		token = h
	}
	if m.Protocol == "sc3" && !strings.HasPrefix(token, "sctp"+p.str("_sc3_uid")+"t") {
		return nil, "", "", bad("SC3 UID 与 SendKey 不匹配")
	}
	if e = validateMessage(m); e != nil {
		return nil, "", "", e
	}
	return m, token, kind, nil
}

func success(w http.ResponseWriter, m *Message) {
	switch m.Protocol {
	case "serverchan", "sc3", "pushbear":
		respond(w, 200, map[string]any{"code": 0, "message": "成功", "data": map[string]any{"id": m.ID}, "warnings": m.Warnings})
	case "pushplus":
		respond(w, 200, map[string]any{"code": 200, "msg": "请求成功", "data": map[string]string{"messageId": m.ID}, "warnings": m.Warnings})
	case "bark":
		respond(w, 200, map[string]any{"code": 200, "message": "success", "timestamp": m.CreatedAt, "data": map[string]string{"message_id": m.ID}, "warnings": m.Warnings})
	case "wxpusher":
		data := []any{}
		for _, uid := range m.UIDs {
			data = append(data, map[string]any{"uid": uid, "messageContentId": m.ContentID, "code": 1000, "status": "创建发送任务成功"})
		}
		respond(w, 200, map[string]any{"code": 1000, "msg": "处理成功", "success": true, "data": data, "message_id": m.ID, "warnings": m.Warnings})
	default:
		respond(w, 202, map[string]any{"message_id": m.ID, "status": "accepted", "warnings": m.Warnings})
	}
}
