package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Message struct {
	Policy         string          `json:"delivery_policy,omitempty"`
	PolicySnapshot *DeliveryPolicy `json:"delivery_policy_snapshot,omitempty"`
	Test           bool            `json:"is_test,omitempty"`
	BusinessName   string          `json:"business_name,omitempty"`
	IconURL        string          `json:"icon_url,omitempty"`
	Sequence       string          `json:"sequence_id,omitempty"`
	Ntfy           params          `json:"ntfy,omitempty"`
	ContentID      int64           `json:"message_content_id,omitempty"`
	Group          string          `json:"group,omitempty"`
	ID             string          `json:"message_id"`
	UserID         string          `json:"user_id"`
	Devices        []string        `json:"device_ids"`
	UIDs           []string        `json:"uids,omitempty"`
	TopicIDs       []int64         `json:"topic_ids,omitempty"`
	Topic          string          `json:"topic"`
	Business       string          `json:"business_type"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	ContentType    string          `json:"content_type"`
	Priority       int             `json:"priority"`
	Icon           string          `json:"icon"`
	Sound          string          `json:"sound"`
	Click          string          `json:"click_url"`
	Tags           []string        `json:"tags"`
	CreatedAt      int64           `json:"created_at"`
	Protocol       string          `json:"source_protocol"`
	Warnings       []string        `json:"warnings,omitempty"`
	Callback       string          `json:"callback_url,omitempty"`
	Expires        int64           `json:"expires_at,omitempty"`
}
type Targets struct {
	Devices []string `json:"device_ids"`
	Topics  []int64  `json:"topic_ids"`
}
type Key struct {
	ID, UserID, Kind string
	Targets          Targets
}
type AppError struct {
	Status     int
	Code, Text string
}

func (e *AppError) Error() string { return e.Text }
func bad(s string) error          { return &AppError{400, "invalid_request", s} }
func forbidden() error            { return &AppError{403, "forbidden", "无权访问指定目标"} }
func secret(prefix string) string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func uuid() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }

var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var businesses = map[string]bool{"server": true, "security": true, "email": true, "task": true, "network": true, "custom": true}
var sounds = map[string]bool{"default": true, "alarm": true, "soft": true, "silent": true}

func validateMessage(m *Message) error {
	if m.Ntfy != nil {
		for field := range m.Ntfy {
			if field != "attachment" && field != "actions" {
				return bad("ntfy扩展只支持attachment和actions")
			}
		}
		if len(encode(m.Ntfy)) > 16384 {
			return bad("附件和操作配置过大")
		}
		if attachment, exists := m.Ntfy["attachment"]; exists {
			a, ok := attachment.(map[string]any)
			if !ok {
				return bad("附件配置需要JSON对象")
			}
			u, e := url.Parse(params(a).str("url"))
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
				return bad("附件需要HTTPS地址")
			}
			if len(params(a).str("name")) > 255 {
				return bad("附件名称过长")
			}
		}
		if actions, exists := m.Ntfy["actions"]; exists {
			a, ok := actions.([]any)
			if !ok || len(a) > 3 {
				return bad("最多3个通知操作")
			}
			for _, raw := range a {
				action, ok := raw.(map[string]any)
				if !ok {
					return bad("操作需要JSON对象")
				}
				p := params(action)
				if p.str("label") == "" || len(p.str("label")) > 100 {
					return bad("操作需要有效的显示名称")
				}
				if p.str("action") != "view" && p.str("action") != "http" {
					return bad("球球操作支持view和http；完整ntfy操作使用ntfy原生接口")
				}
				u, e := url.Parse(p.str("url"))
				if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
					return bad("通知操作需要HTTPS地址")
				}
			}
		}
	}
	if len(m.BusinessName) > 100 {
		return bad("业务显示名过长")
	}
	if m.IconURL != "" {
		u, e := url.Parse(m.IconURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(m.IconURL) > 2048 {
			return bad("自定义图标必须是 HTTPS 地址")
		}
	}
	if strings.TrimSpace(m.Body) == "" {
		return bad("消息正文不能为空")
	}
	if len(m.Body) > 65535 || len([]rune(m.Body)) > 40000 {
		return bad("正文不能超过 65535 UTF-8 字节或 40000 字符")
	}
	if len(m.Title) > 1024 {
		return bad("标题过长")
	}
	if m.Priority == 0 {
		m.Priority = 3
	}
	if m.Priority < 1 || m.Priority > 5 {
		return bad("priority 必须为 1–5")
	}
	if m.Business == "" {
		m.Business = "custom"
	}
	if !businesses[m.Business] {
		return bad("未知业务类型")
	}
	if m.Icon == "" {
		m.Icon = m.Business
	}
	if !businesses[m.Icon] {
		return bad("icon 必须是本地图标别名")
	}
	if m.Sound == "" {
		m.Sound = "default"
	}
	if !sounds[m.Sound] {
		return bad("sound 必须是 default/alarm/soft/silent")
	}
	if m.ContentType == "" {
		m.ContentType = "text"
	}
	if m.ContentType != "text" && m.ContentType != "markdown" && m.ContentType != "html" {
		return bad("未知内容格式")
	}
	if m.Topic != "" && !topicPattern.MatchString(m.Topic) {
		return bad("Topic 格式无效")
	}
	if m.Click != "" {
		u, e := url.Parse(m.Click)
		if e != nil || u.Scheme == "" || strings.ContainsAny(m.Click, "\r\n") || len(m.Click) > 2048 {
			return bad("跳转 URL 无效")
		}
		switch strings.ToLower(u.Scheme) {
		case "javascript", "data", "file", "content", "intent":
			return bad("禁止的跳转协议")
		}
		if (u.Scheme == "https" || u.Scheme == "http") && (u.Hostname() == "" || u.User != nil) {
			return bad("跳转 URL 无效")
		}
	}
	if len(m.Tags) > 32 {
		return bad("标签过多")
	}
	for _, s := range m.Tags {
		if len(s) > 128 {
			return bad("标签过长")
		}
	}
	m.ID = uuid()
	m.CreatedAt = time.Now().Unix()
	return nil
}
