package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type session struct {
	ID, UserID, AppID string
	User              User
}
type handler func(http.ResponseWriter, *http.Request, *session)

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, message string) {
	jsonOut(w, status, map[string]string{"error": message})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		fail(w, 400, "请求格式不正确")
		return false
	}
	if e := d.Decode(&struct{}{}); !errors.Is(e, io.EOF) {
		fail(w, 400, "请求格式不正确")
		return false
	}
	return true
}
func ip(r *http.Request) string {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		return r.RemoteAddr
	}
	return host
}
func emailValid(s string) bool {
	m, e := mail.ParseAddress(s)
	return e == nil && m.Address == s && len(s) <= 254
}
func (a *App) limited(r *http.Request) bool {
	a.limitMu.Lock()
	defer a.limitMu.Unlock()
	t := time.Now()
	key := ip(r)
	b := a.limits[key]
	if t.Sub(b.Start) > time.Minute {
		b = bucket{Start: t}
	}
	b.Count++
	a.limits[key] = b
	if len(a.limits) > 10000 {
		for k, v := range a.limits {
			if t.Sub(v.Start) > time.Minute {
				delete(a.limits, k)
			}
		}
	}
	return b.Count > 30
}
func (a *App) session(r *http.Request, console bool) (*session, error) {
	var raw string
	if console {
		c, e := r.Cookie("auth_session")
		if e != nil {
			return nil, e
		}
		raw = c.Value
	} else {
		raw = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	var s session
	var exp int64
	err := a.db.QueryRow("SELECT id,user_id,app_id,expires_at FROM sessions WHERE token_hash=?", hash(raw)).Scan(&s.ID, &s.UserID, &s.AppID, &exp)
	if err != nil || exp <= now() || (console && s.AppID != "console") {
		return nil, errors.New("unauthorized")
	}
	u, e := a.user(s.UserID)
	if e != nil || u.Disabled {
		return nil, errors.New("unauthorized")
	}
	s.User = u
	return &s, nil
}
func (a *App) wrap(h handler, role string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, e := a.session(r, true)
		if e != nil {
			fail(w, 401, "请先登录")
			return
		}
		if role == "admin" && s.User.Role != "admin" {
			fail(w, 403, "需要管理员权限")
			return
		}
		if role == "developer" && s.User.Role != "admin" && s.User.Role != "developer" {
			fail(w, 403, "需要开发者或管理员权限")
			return
		}
		h(w, r, s)
	}
}
func (a *App) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: "auth_session", Value: value, Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookie, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.db.PingContext(ctx); err != nil {
			fail(w, http.StatusServiceUnavailable, "服务暂不可用")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("GET /api/status", a.status)
	m.HandleFunc("POST /api/setup", a.setup)
	m.HandleFunc("POST /api/auth/login", a.login)
	m.HandleFunc("POST /api/auth/forgot", a.forgot)
	m.HandleFunc("POST /api/auth/reset", a.reset)
	m.HandleFunc("POST /api/auth/appeal", a.appeal)
	m.HandleFunc("GET /api/me", a.wrap(a.me, ""))
	m.HandleFunc("POST /api/auth/logout", a.wrap(a.logout, ""))
	m.HandleFunc("POST /api/heartbeat", a.wrap(a.heartbeat, ""))
	m.HandleFunc("GET /api/sessions", a.wrap(a.sessions, ""))
	m.HandleFunc("DELETE /api/sessions/{id}", a.wrap(a.revokeSession, ""))
	m.HandleFunc("POST /api/password", a.wrap(a.changePassword, ""))
	m.HandleFunc("POST /api/2fa/setup", a.wrap(a.totpSetup, ""))
	m.HandleFunc("POST /api/2fa/enable", a.wrap(a.totpEnable, ""))
	m.HandleFunc("POST /api/2fa/disable", a.wrap(a.totpDisable, ""))
	m.HandleFunc("GET /api/applications", a.wrap(a.applications, "developer"))
	m.HandleFunc("POST /api/applications", a.wrap(a.createApplication, "developer"))
	m.HandleFunc("GET /api/keys", a.wrap(a.keys, "developer"))
	m.HandleFunc("POST /api/keys", a.wrap(a.createKey, "developer"))
	m.HandleFunc("DELETE /api/keys/{id}", a.wrap(a.revokeKey, "developer"))
	m.HandleFunc("GET /api/admin/users", a.wrap(a.users, "admin"))
	m.HandleFunc("PATCH /api/admin/users/{id}", a.wrap(a.updateUser, "admin"))
	m.HandleFunc("GET /api/admin/mail", a.wrap(a.getMail, "admin"))
	m.HandleFunc("PUT /api/admin/mail", a.wrap(a.putMail, "admin"))
	m.HandleFunc("POST /api/admin/mail/test", a.wrap(a.testMail, "admin"))
	m.HandleFunc("GET /api/admin/appeals", a.wrap(a.appeals, "admin"))
	m.HandleFunc("POST /api/admin/appeals/{id}/resolve", a.wrap(a.resolveAppeal, "admin"))
	m.HandleFunc("GET /api/audit", a.wrap(a.audits, ""))
	m.HandleFunc("POST /v1/auth/register", a.thirdRegister)
	m.HandleFunc("POST /v1/auth/login", a.thirdLogin)
	m.HandleFunc("POST /v1/session/heartbeat", a.thirdHeartbeat)
	m.HandleFunc("GET /v1/session", a.thirdSession)
	m.HandleFunc("POST /v1/auth/logout", a.thirdLogout)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			fail(w, 404, "接口不存在")
			return
		}
		path := filepath.Join(a.cfg.WebDir, filepath.Clean("/"+r.URL.Path))
		if st, e := os.Stat(path); e == nil && !st.IsDir() {
			http.ServeFile(w, r, path)
			return
		}
		http.ServeFile(w, r, filepath.Join(a.cfg.WebDir, "index.html"))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != "GET" && r.Method != "HEAD" {
			if o := r.Header.Get("Origin"); o != "" {
				u, e := url.Parse(o)
				expected, _ := url.Parse(a.cfg.PublicURL)
				if e != nil || !(u.Host == r.Host || (expected != nil && o == expected.Scheme+"://"+expected.Host)) {
					fail(w, 403, "请求来源不受信任")
					return
				}
			} else if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, 403, "请求来源不受信任")
				return
			}
		}
		if r.Method == "POST" && (strings.HasPrefix(r.URL.Path, "/api/auth/") || strings.HasPrefix(r.URL.Path, "/api/2fa/") || r.URL.Path == "/api/password" || r.URL.Path == "/api/setup" || strings.HasPrefix(r.URL.Path, "/v1/")) {
			if a.limited(r) {
				fail(w, 429, "请求过于频繁，请一分钟后重试")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
