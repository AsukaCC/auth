package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/pquerna/otp/totp"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testClient struct {
	t           *testing.T
	a           *App
	cookie      *http.Cookie
	key, bearer string
}

func newTest(t *testing.T) *testClient {
	t.Helper()
	a, e := New(Config{DataDir: t.TempDir(), PublicURL: "http://auth.test", WebDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	return &testClient{t: t, a: a}
}
func (c *testClient) request(method, path string, data any, want int) map[string]any {
	c.t.Helper()
	body, _ := json.Marshal(data)
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	if c.key != "" {
		r.Header.Set("X-API-Key", c.key)
	}
	if c.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	w := httptest.NewRecorder()
	c.a.Handler().ServeHTTP(w, r)
	if w.Code != want {
		c.t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	for _, v := range w.Result().Cookies() {
		if v.Name == "auth_session" {
			c.cookie = v
		}
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}
func (c *testClient) setup() string {
	b := c.request("POST", "/api/setup", map[string]string{"email": "admin@example.com", "name": "Admin", "password": "admin-password-123"}, 200)
	return b["user"].(map[string]any)["id"].(string)
}
func (c *testClient) app(name string) string {
	return c.request("POST", "/api/applications", map[string]string{"name": name, "description": "test application"}, 201)["id"].(string)
}
func (c *testClient) makeKey(aid string, scopes ...string) string {
	return c.request("POST", "/api/keys", map[string]any{"app_id": aid, "name": "test key", "scopes": scopes}, 201)["key"].(string)
}
func TestRolesScopesAndSessionIsolation(t *testing.T) {
	c := newTest(t)
	c.setup()
	c.request("POST", "/api/setup", map[string]string{"email": "other@example.com", "name": "Other", "password": "password-12345"}, 409)
	adminCookie := c.cookie
	app1 := c.app("One")
	app2 := c.app("Two")
	registerKey := c.makeKey(app1, "auth:register")
	key1 := c.makeKey(app1, "auth:login", "session:read")
	key2 := c.makeKey(app2, "auth:login", "session:read")
	c.key = registerKey
	c.request("POST", "/v1/auth/register", map[string]string{"email": "member@example.com", "name": "Member", "password": "member-password"}, 201)
	c.request("POST", "/v1/auth/login", map[string]string{"email": "member@example.com", "password": "member-password"}, 403)
	c.key = key1
	b := c.request("POST", "/v1/auth/login", map[string]string{"email": "member@example.com", "password": "member-password"}, 200)
	uid := b["user"].(map[string]any)["id"].(string)
	c.bearer = b["access_token"].(string)
	c.request("GET", "/v1/session", nil, 200)
	c.key = key2
	c.request("GET", "/v1/session", nil, 401)
	c.key = key1
	c.cookie = &http.Cookie{Name: "auth_session", Value: c.bearer}
	c.request("GET", "/api/me", nil, 401)
	c.cookie = nil
	c.request("POST", "/api/auth/login", map[string]string{"email": "member@example.com", "password": "member-password"}, 200)
	memberCookie := c.cookie
	c.request("POST", "/api/applications", map[string]string{"name": "Forbidden"}, 403)
	c.request("GET", "/api/admin/users", nil, 403)
	c.cookie = adminCookie
	c.request("PATCH", "/api/admin/users/"+uid, map[string]any{"role": "developer", "disabled": false}, 200)
	c.cookie = memberCookie
	owned := c.app("Member application")
	c.makeKey(owned, "auth:login")
	c.request("POST", "/api/keys", map[string]any{"app_id": app1, "name": "stolen", "scopes": []string{"auth:login"}}, 403)
	c.cookie = adminCookie
	c.request("PATCH", "/api/admin/users/"+uid, map[string]any{"role": "user", "disabled": false}, 200)
	c.cookie = memberCookie
	c.request("GET", "/api/keys", nil, 403)
	c.cookie = adminCookie
	var kid string
	_ = c.a.db.QueryRow("SELECT id FROM api_keys WHERE hash=?", hash(key1)).Scan(&kid)
	c.request("DELETE", "/api/keys/"+kid, nil, 200)
	c.request("GET", "/v1/session", nil, 401)
}
func TestTwoFactorReplayAndRecoveryCodes(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	b := c.request("POST", "/api/2fa/setup", map[string]string{"password": "admin-password-123"}, 200)
	secret := b["secret"].(string)
	code, _ := totp.GenerateCode(secret, time.Now())
	b = c.request("POST", "/api/2fa/enable", map[string]string{"code": code}, 200)
	codes := b["backup_codes"].([]any)
	if len(codes) != 8 {
		t.Fatal("expected 8 backup codes")
	}
	if c.a.verifyCode(uid, code) {
		t.Fatal("activation code replayed")
	}
	c.cookie = nil
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123"}, 401)
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123", "code": codes[0].(string)}, 200)
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123", "code": codes[0].(string)}, 401)
	c.request("POST", "/api/2fa/disable", map[string]string{"password": "admin-password-123", "code": codes[1].(string)}, 200)
	u, _ := c.a.user(uid)
	if u.TFA {
		t.Fatal("2fa remains active")
	}
	var n int
	_ = c.a.db.QueryRow("SELECT COUNT(*) FROM backup_codes WHERE user_id=?", uid).Scan(&n)
	if n != 0 {
		t.Fatal("backup codes remain")
	}
}
func TestAppealAndSingleUseRecovery(t *testing.T) {
	c := newTest(t)
	c.setup()
	c.request("POST", "/api/auth/forgot", map[string]string{"email": "admin@example.com"}, 409)
	c.request("POST", "/api/auth/appeal", map[string]string{"email": "admin@example.com", "contact": "verified phone", "reason": "lost password"}, 200)
	var appealID string
	_ = c.a.db.QueryRow("SELECT id FROM appeals").Scan(&appealID)
	b := c.request("POST", "/api/admin/appeals/"+appealID+"/resolve", map[string]any{"approve": true, "note": "Identity verified", "reset_2fa": false}, 200)
	link, _ := url.Parse(b["recovery_url"].(string))
	raw := link.Query().Get("reset")
	c.request("GET", "/api/me", nil, 200)
	c.request("POST", "/api/auth/reset", map[string]string{"token": raw, "password": "new-password-123"}, 200)
	c.request("GET", "/api/me", nil, 401)
	c.request("POST", "/api/auth/reset", map[string]string{"token": raw, "password": "another-password-123"}, 400)
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123"}, 401)
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "new-password-123"}, 200)
}
func TestMailSecretEncryptionPersistenceAndExpiry(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	m := map[string]any{"enabled": true, "host": "smtp.example.com", "port": 587, "username": "mailer", "password": "smtp-secret", "from": "noreply@example.com", "tls": "starttls"}
	c.request("PUT", "/api/admin/mail", m, 200)
	b := c.request("GET", "/api/admin/mail", nil, 200)
	if b["password"] != nil || b["has_password"] != true {
		t.Fatal("mail password exposure")
	}
	var stored string
	_ = c.a.db.QueryRow("SELECT value FROM settings WHERE key='mail'").Scan(&stored)
	if strings.Contains(stored, "smtp-secret") {
		t.Fatal("plaintext secret stored")
	}
	m["password"] = ""
	c.request("PUT", "/api/admin/mail", m, 200)
	cfg, _ := c.a.mailConfig()
	if cfg.Password != "smtp-secret" {
		t.Fatal("empty update should preserve password")
	}
	raw, e := c.a.newRecovery(uid)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = c.a.db.Exec("UPDATE recovery SET expires_at=?", now()-1)
	c.request("POST", "/api/auth/reset", map[string]string{"token": raw, "password": "new-password-123"}, 400)
	keyPath := filepath.Join(c.a.cfg.DataDir, "master.key")
	st, e := os.Stat(keyPath)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("master key permissions")
	}
	other, e := New(c.a.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	cfg, e = other.mailConfig()
	if e != nil || cfg.Password != "smtp-secret" {
		t.Fatal("cannot decrypt after reopening")
	}
}
func TestOriginRateLimitDisabledAndExpiredSessions(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	r := httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.AddCookie(c.cookie)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	c.a.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	_, _ = c.a.db.Exec("UPDATE sessions SET expires_at=?", now()-1)
	c.request("GET", "/api/me", nil, 401)
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123"}, 200)
	_, _ = c.a.db.Exec("UPDATE users SET disabled=1 WHERE id=?", uid)
	c.request("GET", "/api/me", nil, 401)
	for i := 0; i < 31; i++ {
		r := httptest.NewRequest("POST", "/api/auth/login", bytes.NewBufferString("{}"))
		r.RemoteAddr = "198.51.100.1:123"
		w := httptest.NewRecorder()
		c.a.Handler().ServeHTTP(w, r)
		if i == 30 && w.Code != 429 {
			t.Fatal(fmt.Sprintf("rate limit returned %d", w.Code))
		}
	}
}

func TestEnrollmentFutureCodeCannotReplay(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	b := c.request("POST", "/api/2fa/setup", map[string]string{"password": "admin-password-123"}, 200)
	code, _ := totp.GenerateCode(b["secret"].(string), time.Now().Add(30*time.Second))
	c.request("POST", "/api/2fa/enable", map[string]string{"code": code}, 200)
	if c.a.verifyCode(uid, code) {
		t.Fatal("future-window enrollment code replayed")
	}
}

func TestRecoveryPreserves2FAUnlessAdminExplicitlyResetsIt(t *testing.T) {
	c := newTest(t)
	c.setup()
	admin := c.cookie
	appID := c.app("Recovery test")
	c.key = c.makeKey(appID, "auth:register")
	b := c.request("POST", "/v1/auth/register", map[string]string{"email": "recover@example.com", "name": "Recover", "password": "original-password"}, 201)
	uid := b["user"].(map[string]any)["id"].(string)
	c.request("POST", "/api/auth/login", map[string]string{"email": "recover@example.com", "password": "original-password"}, 200)
	b = c.request("POST", "/api/2fa/setup", map[string]string{"password": "original-password"}, 200)
	code, _ := totp.GenerateCode(b["secret"].(string), time.Now())
	b = c.request("POST", "/api/2fa/enable", map[string]string{"code": code}, 200)
	backup := b["backup_codes"].([]any)[0].(string)
	raw, e := c.a.newRecovery(uid)
	if e != nil {
		t.Fatal(e)
	}
	c.request("POST", "/api/auth/reset", map[string]string{"token": raw, "password": "new-password-123"}, 200)
	u, _ := c.a.user(uid)
	if !u.TFA {
		t.Fatal("password recovery removed 2FA")
	}
	c.request("POST", "/api/auth/login", map[string]string{"email": "recover@example.com", "password": "new-password-123"}, 401)
	c.request("POST", "/api/auth/login", map[string]string{"email": "recover@example.com", "password": "new-password-123", "code": backup}, 200)
	member := c.cookie
	c.request("POST", "/api/auth/appeal", map[string]string{"email": "recover@example.com", "contact": "verified contact", "reason": "lost authenticator"}, 200)
	var appealID string
	_ = c.a.db.QueryRow("SELECT id FROM appeals WHERE email='recover@example.com'").Scan(&appealID)
	c.cookie = admin
	c.request("POST", "/api/admin/appeals/"+appealID+"/resolve", map[string]any{"approve": true, "note": "Identity independently verified", "reset_2fa": true}, 200)
	u, _ = c.a.user(uid)
	if u.TFA {
		t.Fatal("explicit admin recovery did not clear 2FA")
	}
	var count int
	_ = c.a.db.QueryRow("SELECT COUNT(*) FROM backup_codes WHERE user_id=?", uid).Scan(&count)
	if count != 0 {
		t.Fatal("backup codes survived 2FA recovery")
	}
	c.cookie = member
	c.request("GET", "/api/me", nil, 401)
}
