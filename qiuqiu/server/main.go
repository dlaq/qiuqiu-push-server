package main

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Config struct{ Listen, Database, NtfyURL, NtfyPublicURL, NtfyHost, AdminDir, AdminName, AdminPassword, NtfyAdminName, NtfyAdminPassword, NtfySocket, AllowedCallbacks string }
type Server struct {
	store           *Store
	cfg             Config
	client          *http.Client
	adminClient     *http.Client
	wake            chan struct{}
	provision       sync.Mutex
	limiterMu       sync.Mutex
	buckets         map[string]bucket
	directMu        sync.Mutex
	direct          map[string]*directClient
	externalClients map[string]*directClient
	channelKey      []byte
}
type bucket struct {
	Time  time.Time
	Count int
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func secretFile(k string) string {
	p := os.Getenv(k)
	if p == "" {
		return ""
	}
	b, e := os.ReadFile(p)
	if e != nil {
		slog.Error("无法读取凭证文件", "setting", k)
		os.Exit(1)
	}
	return strings.TrimSpace(string(b))
}
func newServer(cfg Config) (*Server, error) {
	if cfg.Database != ":memory:" {
		if e := os.MkdirAll(filepath.Dir(cfg.Database), 0700); e != nil {
			return nil, e
		}
	}
	st, e := openStore(cfg.Database)
	if e != nil {
		return nil, e
	}
	s := &Server{store: st, cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, wake: make(chan struct{}, 1), buckets: map[string]bucket{}}
	s.direct = map[string]*directClient{}
	s.externalClients = map[string]*directClient{}
	if e = s.initChannels(); e != nil {
		st.Close()
		return nil, e
	}
	s.adminClient = s.client
	if cfg.NtfySocket != "" {
		s.adminClient = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", cfg.NtfySocket)
		}}}
	}
	var n int
	if e = st.QueryRow("SELECT count(*) FROM users").Scan(&n); e != nil {
		st.Close()
		return nil, e
	}
	if n == 0 {
		if cfg.AdminPassword == "" {
			st.Close()
			return nil, errors.New("首次启动必须设置 QQ_ADMIN_PASSWORD_FILE（密码至少12字节）")
		}
		if _, e = st.addUser(first(cfg.AdminName, "admin"), cfg.AdminPassword, "admin"); e != nil {
			st.Close()
			return nil, e
		}
	}
	return s, nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(encode(v)))
}
func fail(w http.ResponseWriter, r *http.Request, e error) {
	a := &AppError{500, "internal_error", "内部处理失败"}
	var typed *AppError
	if errors.As(dbError(e), &typed) {
		a = typed
	}
	code := a.Status
	if a.Status >= 400 {
		switch {
		case strings.HasPrefix(r.URL.Path, "/sctapi/") || strings.HasPrefix(r.URL.Path, "/sc3/") || r.URL.Path == "/pushbear/sub":
			code = -1
		case r.URL.Path == "/api/send/message":
			code = 1001
		}
	}
	respond(w, a.Status, map[string]any{"code": code, "error": a.Code, "message": a.Text, "msg": a.Text, "success": false})
}
func (s *Server) limit(key string, max int) bool {
	s.limiterMu.Lock()
	defer s.limiterMu.Unlock()
	now := time.Now()
	if len(s.buckets) > 10000 {
		for k, b := range s.buckets {
			if now.Sub(b.Time) > time.Minute {
				delete(s.buckets, k)
			}
		}
		if len(s.buckets) > 10000 {
			return false
		}
	}
	b := s.buckets[key]
	if now.Sub(b.Time) > time.Minute {
		b = bucket{Time: now}
	}
	b.Count++
	s.buckets[key] = b
	return b.Count <= max
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	if s.cfg.NtfyHost != "" && strings.EqualFold(strings.Split(r.Host, ":")[0], s.cfg.NtfyHost) {
		s.nativeProxy(w, r)
		return
	}
	if r.URL.Path == "/healthz" {
		if e := s.store.PingContext(r.Context()); e != nil {
			respond(w, 503, map[string]bool{"healthy": false})
		} else {
			respond(w, 200, map[string]bool{"healthy": true})
		}
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/s/") {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		if s.cfg.AdminDir == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.cfg.AdminDir, "index.html"))
		return
	}
	if compatibilityPath(r) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !s.limit("compat:"+ip, 120) {
			fail(w, r, &AppError{429, "rate_limited", "请求过于频繁"})
			return
		}
		if e := s.compatibilityHTTP(w, r); e != nil {
			fail(w, r, e)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/messages/") {
		if e := s.senderMessageHTTP(w, r); e != nil {
			fail(w, r, e)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/icons/") {
		s.iconHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/onebot/reverse/") {
		s.oneBotReverseHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/register_client/") {
		s.legacyClientHTTP(w, r)
		return
	}
	if r.URL.Path == "/api/v1/ws" || r.URL.Path == "/api/v1/device/ws" || r.URL.Path == "/ws" {
		s.websocketHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/device/") {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if e := s.deviceHTTP(w, r); e != nil {
			fail(w, r, e)
		}
		return
	}
	if r.URL.Path == "/api/v1/login" || r.URL.Path == "/api/v1/pair/claim" {
		if !s.limit("login:"+ip, 20) {
			fail(w, r, &AppError{429, "rate_limited", "请求过于频繁"})
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/manage/") || r.URL.Path == "/api/v1/login" || r.URL.Path == "/api/v1/logout" || r.URL.Path == "/api/v1/me" || r.URL.Path == "/api/v1/pair/claim" {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if e := s.manage(w, r); e != nil {
			fail(w, r, e)
		}
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/" || r.URL.Path == "/brand.png" || r.URL.Path == "/favicon.ico" || strings.HasPrefix(r.URL.Path, "/admin") || strings.HasPrefix(r.URL.Path, "/assets/")) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		if s.cfg.AdminDir == "" {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin") {
			http.ServeFile(w, r, filepath.Join(s.cfg.AdminDir, "index.html"))
		} else {
			http.FileServer(http.Dir(s.cfg.AdminDir)).ServeHTTP(w, r)
		}
		return
	}
	segment := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
	reserved := map[string]bool{"api": true, "sctapi": true, "sc3": true, "bark": true, "pushbear": true, "send": true, "push": true, "admin": true, "assets": true, "healthz": true}
	if !reserved[segment] {
		s.nativeProxy(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if handled, err := s.compatibilityBatch(w, r); handled {
		if err != nil {
			fail(w, r, err)
		}
		return
	}
	m, token, kind, e := adaptWithDecrypt(r, s.decryptBark)
	if e != nil {
		fail(w, r, e)
		return
	}
	k, e := s.store.key(token, kind)
	if e != nil {
		fail(w, r, e)
		return
	}
	s.store.usage(k)
	if m.Policy == "" {
		_ = s.store.QueryRow("SELECT policy_id FROM key_routes WHERE key_id=?", k.ID).Scan(&m.Policy)
	}
	if e = s.checkPolicy(k.UserID, m); e != nil {
		fail(w, r, e)
		return
	}
	if !s.limit("key:"+k.ID, 120) {
		fail(w, r, &AppError{429, "rate_limited", "每个凭证每分钟最多120次请求"})
		return
	}
	if e = s.checkCallback(m.Callback); e != nil {
		fail(w, r, e)
		return
	}
	if m.Expires > 0 && m.Expires <= time.Now().Unix() {
		fail(w, r, bad("消息已过期"))
		return
	}
	devices, e := s.store.resolve(k, m)
	if e != nil {
		fail(w, r, e)
		return
	}
	if m.ContentType == "html" {
		m.Body = htmlPolicy.Sanitize(m.Body)
	}
	idem := ""
	if m.Protocol == "native" {
		idem = r.Header.Get("Idempotency-Key")
	}
	m, e = s.store.accept(k, m, devices, idem)
	if e != nil {
		fail(w, r, e)
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	success(w, m)
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		raw, e := io.ReadAll(io.LimitReader(os.Stdin, 73))
		if e != nil || len(raw) < 12 || len(raw) > 72 {
			os.Exit(1)
		}
		h, e := bcrypt.GenerateFromPassword(raw, bcrypt.DefaultCost)
		if e != nil {
			os.Exit(1)
		}
		fmt.Print(string(h))
		return
	}

	cfg := Config{Listen: env("QQ_LISTEN", ":8080"), Database: env("QQ_DATABASE", "data/qiuqiu.db"), NtfyURL: env("QQ_NTFY_URL", "http://ntfy:80"), NtfyPublicURL: env("QQ_NTFY_PUBLIC_URL", ""), NtfyHost: env("QQ_NTFY_HOST", ""), AdminDir: env("QQ_ADMIN_DIR", "admin-dist"), AdminName: env("QQ_ADMIN_NAME", "admin"), AdminPassword: secretFile("QQ_ADMIN_PASSWORD_FILE"), NtfyAdminName: env("QQ_NTFY_ADMIN_NAME", "gateway"), NtfyAdminPassword: secretFile("QQ_NTFY_ADMIN_PASSWORD_FILE"), NtfySocket: env("QQ_NTFY_ADMIN_SOCKET", ""), AllowedCallbacks: env("QQ_CALLBACK_ALLOWLIST", "")}
	s, e := newServer(cfg)
	if e != nil {
		slog.Error(e.Error())
		os.Exit(1)
	}
	defer s.store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go s.runWorker(ctx)
	srv := &http.Server{Addr: cfg.Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	slog.Info("球球推送已启动", "listen", cfg.Listen)
	if e = srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		slog.Error("HTTP 服务异常")
		os.Exit(1)
	}
}
