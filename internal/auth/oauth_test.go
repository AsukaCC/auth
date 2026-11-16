package auth

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

type oauthTransport func(*http.Request) (*http.Response, error)

func (fn oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type providerFixture struct {
	t              *testing.T
	subject, email string
	verified       bool
	challenge      string
	calls          int
}

func (p *providerFixture) respond(r *http.Request) (*http.Response, error) {
	p.calls++
	if r.URL.Scheme != "https" {
		p.t.Fatal("provider must use HTTPS")
	}
	var body string
	switch r.URL.String() {
	case oauthProviders["google"].token, oauthProviders["github"].token:
		if r.Method != "POST" {
			p.t.Fatal("token endpoint must receive POST")
		}
		if err := r.ParseForm(); err != nil {
			p.t.Fatal(err)
		}
		if r.Form.Get("client_secret") != "provider-secret" || r.Form.Get("client_id") != "client-id" || oauthChallenge(r.Form.Get("code_verifier")) != p.challenge || r.Form.Get("code") != "provider-code" {
			p.t.Fatal("invalid provider token request", r.Form)
		}
		body = `{"access_token":"provider-access-token","token_type":"Bearer"}`
	case oauthProviders["google"].profile:
		b, _ := json.Marshal(map[string]any{"sub": p.subject, "email": p.email, "email_verified": p.verified, "name": "OAuth 用户"})
		body = string(b)
	case oauthProviders["github"].profile:
		body = `{"id":123456789,"login":"github-user","name":"GitHub 用户","email":null}`
	case "https://api.github.com/user/emails":
		b, _ := json.Marshal([]map[string]any{{"email": p.email, "primary": true, "verified": p.verified}, {"email": "secondary@example.test", "primary": false, "verified": true}})
		body = string(b)
	default:
		p.t.Fatal("unexpected provider request", r.URL)
	}
	if r.Method == "GET" && r.Header.Get("Authorization") != "Bearer provider-access-token" {
		p.t.Fatal("missing resource access token")
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func oauthFixture(t *testing.T, c *testClient, provider string, signup bool) *providerFixture {
	t.Helper()
	c.request("PUT", "/api/admin/oauth/"+provider, map[string]any{"enabled": true, "client_id": "client-id", "client_secret": "provider-secret", "allow_signup": signup, "expected_revision": "", "password": "admin-password-123"}, 200)
	p := &providerFixture{t: t, subject: "google-subject-001", email: "admin@example.com", verified: true}
	c.a.oauthClient = &http.Client{Transport: oauthTransport(p.respond)}
	return p
}

type oauthBrowser struct {
	c       *testClient
	cookies map[string]*http.Cookie
}

func newOAuthBrowser(c *testClient, console bool) *oauthBrowser {
	b := &oauthBrowser{c: c, cookies: map[string]*http.Cookie{}}
	if console {
		b.cookies[c.cookie.Name] = c.cookie
	}
	return b
}
func (b *oauthBrowser) do(method, path string, data any, want int) *httptest.ResponseRecorder {
	b.c.t.Helper()
	raw, _ := json.Marshal(data)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.RemoteAddr = "192.0.2.99:1234"
	for _, c := range b.cookies {
		if c.Name == "auth_session" && strings.Contains(path, "/callback?") {
			continue
		}
		r.AddCookie(c)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://auth.test")
	w := httptest.NewRecorder()
	b.c.a.Handler().ServeHTTP(w, r)
	if w.Code != want {
		b.c.t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
		if c.Name == "auth_session" {
			b.c.cookie = c
		}
	}
	return w
}
func (b *oauthBrowser) json(method, path string, data any, want int) map[string]any {
	b.c.t.Helper()
	w := b.do(method, path, data, want)
	var v map[string]any
	if json.Unmarshal(w.Body.Bytes(), &v) != nil {
		b.c.t.Fatal(w.Body.String())
	}
	return v
}
func (b *oauthBrowser) start(p *providerFixture, provider, intent string) string {
	b.c.t.Helper()
	in := map[string]string{"intent": intent}
	if intent == "bind" {
		in["password"] = "admin-password-123"
	}
	v := b.json("POST", "/api/auth/oauth/"+provider+"/start", in, 200)
	u, err := url.Parse(v["authorization_url"].(string))
	if err != nil {
		b.c.t.Fatal(err)
	}
	p.challenge = u.Query().Get("code_challenge")
	if u.Query().Get("code_challenge_method") != "S256" || p.challenge == "" || u.Query().Get("redirect_uri") != "http://auth.test/api/auth/oauth/"+provider+"/callback" {
		b.c.t.Fatal("missing PKCE or exact callback", u)
	}
	return u.Query().Get("state")
}
func (b *oauthBrowser) callback(provider, state string) *httptest.ResponseRecorder {
	b.c.t.Helper()
	return b.do("GET", "/api/auth/oauth/"+provider+"/callback?code=provider-code&state="+state, nil, 303)
}
func requireOAuthReturn(t *testing.T, w *httptest.ResponseRecorder, value string) {
	t.Helper()
	if !strings.Contains(w.Header().Get("Location"), value) {
		t.Fatal("unexpected OAuth return", w.Header().Get("Location"))
	}
}
func bindOAuthAdmin(t *testing.T, c *testClient, p *providerFixture, provider string) {
	t.Helper()
	b := newOAuthBrowser(c, true)
	state := b.start(p, provider, "bind")
	requireOAuthReturn(t, b.callback(provider, state), "oauth=complete")
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
}

func TestOAuthBindingLoginAndUnlink(t *testing.T) {
	for _, provider := range []string{"google", "github"} {
		t.Run(provider, func(t *testing.T) {
			c := newTest(t)
			uid := c.setup()
			p := oauthFixture(t, c, provider, false)
			bindOAuthAdmin(t, c, p, provider)
			var subject string
			if err := c.a.db.QueryRow("SELECT subject FROM oauth_identities WHERE user_id=? AND provider=?", uid, provider).Scan(&subject); err != nil || provider == "github" && subject != "123456789" || provider == "google" && subject != p.subject {
				t.Fatal("stable provider identity not stored", subject, err)
			}
			c.request("GET", "/api/account/identities", nil, 200)
			b := newOAuthBrowser(c, false)
			state := b.start(p, provider, "login")
			requireOAuthReturn(t, b.callback(provider, state), "oauth=complete")
			pending := b.json("GET", "/api/auth/oauth/pending", nil, 200)
			if pending["requires_registration"] != false || pending["requires_2fa"] != false {
				t.Fatal(pending)
			}
			logged := b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
			if logged["user"].(map[string]any)["id"] != uid {
				t.Fatal(logged)
			}
			if b.cookies["auth_session"].SameSite != http.SameSiteStrictMode {
				t.Fatal("console cookie policy weakened")
			}
			b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 401)
			c.request("POST", "/api/account/identities/"+provider+"/unlink", map[string]string{"password": "wrong"}, 403)
			c.request("POST", "/api/account/identities/"+provider+"/unlink", map[string]string{"password": "admin-password-123"}, 200)
			var count int
			_ = c.a.db.QueryRow("SELECT COUNT(*) FROM oauth_identities").Scan(&count)
			if count != 0 {
				t.Fatal("identity survived unlink")
			}
			_ = c.a.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id=?", uid).Scan(&count)
			if count != 1 {
				t.Fatal("unlink did not revoke other sessions", count)
			}
		})
	}
}
func TestOAuthRejectsStateProviderAndReplay(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", false)
	b := newOAuthBrowser(c, false)
	state := b.start(p, "google", "login")
	other := newOAuthBrowser(c, false)
	requireOAuthReturn(t, other.callback("google", state), "invalid_state")
	requireOAuthReturn(t, b.callback("github", state), "invalid_state")
	if p.calls != 0 {
		t.Fatal("provider contacted before state validation")
	}
	requireOAuthReturn(t, b.callback("google", state), "binding_required")
	before := p.calls
	requireOAuthReturn(t, b.callback("google", state), "invalid_state")
	if p.calls != before {
		t.Fatal("callback replay reached provider")
	}
	state = b.start(p, "google", "login")
	_, _ = c.a.db.Exec("UPDATE oauth_flows SET expires_at=0")
	requireOAuthReturn(t, b.callback("google", state), "invalid_state")
	if p.calls != before {
		t.Fatal("expired state reached provider")
	}
}
func TestOAuthNoEmailAutoLinkAndRegistrationPolicy(t *testing.T) {
	for _, scenario := range []string{"existing-email", "signup-disabled", "unverified", "signup-enabled"} {
		t.Run(scenario, func(t *testing.T) {
			c := newTest(t)
			c.setup()
			p := oauthFixture(t, c, "google", scenario == "signup-enabled")
			p.email = "new-user@example.test"
			want := "registration_disabled"
			switch scenario {
			case "existing-email":
				p.email = "admin@example.com"
				want = "binding_required"
			case "unverified":
				p.verified = false
				want = "provider_failed"
			case "signup-enabled":
				want = "oauth=complete"
			}
			b := newOAuthBrowser(c, false)
			state := b.start(p, "google", "login")
			requireOAuthReturn(t, b.callback("google", state), want)
			if scenario != "signup-enabled" {
				var count int
				_ = c.a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
				if count != 1 {
					t.Fatal("unauthorized registration")
				}
				return
			}
			b.json("POST", "/api/auth/oauth/complete", map[string]string{"password": "short", "name": "New"}, 400)
			v := b.json("POST", "/api/auth/oauth/complete", map[string]string{"password": "local-password-123", "name": "新用户"}, 200)
			u := v["user"].(map[string]any)
			if u["role"] != "user" || u["email"] != p.email {
				t.Fatal("social registration elevated role", u)
			}
			if !c.a.verifyPassword(u["id"].(string), "local-password-123") {
				t.Fatal("local password not set")
			}
		})
	}
}
func TestOAuthTwoFactorAndSecurityInvalidation(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	p := oauthFixture(t, c, "google", false)
	bindOAuthAdmin(t, c, p, "google")
	_, err := c.a.db.Exec("UPDATE users SET totp=? WHERE id=?", c.a.seal("JBSWY3DPEHPK3PXP"), uid)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.a.db.Exec("INSERT INTO backup_codes VALUES(?,?)", uid, hash("one-time-backup"))
	b := newOAuthBrowser(c, false)
	state := b.start(p, "google", "login")
	requireOAuthReturn(t, b.callback("google", state), "oauth=complete")
	if b.json("GET", "/api/auth/oauth/pending", nil, 200)["requires_2fa"] != true {
		t.Fatal("OAuth bypassed local 2FA")
	}
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 401)
	b.json("POST", "/api/auth/oauth/complete", map[string]string{"code": "one-time-backup"}, 200)
	state = b.start(p, "google", "login")
	b.callback("google", state)
	b.json("POST", "/api/auth/oauth/complete", map[string]string{"code": "one-time-backup"}, 401)
	_, _ = c.a.db.Exec("UPDATE users SET password=? WHERE id=?", "new-security-snapshot", uid)
	b.json("GET", "/api/auth/oauth/pending", nil, 401)
	b.json("POST", "/api/auth/oauth/complete", map[string]string{"code": "one-time-backup"}, 401)
}
func TestOAuthBindNeedsSameActiveConsoleSessionAndUniqueIdentity(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", false)
	b := newOAuthBrowser(c, true)
	b.json("POST", "/api/auth/oauth/google/start", map[string]string{"intent": "bind", "password": "wrong"}, 403)
	state := b.start(p, "google", "bind")
	b.callback("google", state)
	delete(b.cookies, "auth_session")
	b.json("GET", "/api/auth/oauth/pending", nil, 401)
	b.cookies["auth_session"] = c.cookie
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
	state = b.start(p, "google", "bind")
	b.callback("google", state)
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 409)
	state = b.start(p, "google", "bind")
	b.callback("google", state)
	_, _ = c.a.db.Exec("DELETE FROM sessions")
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 401)
}
func TestOAuthPendingAttemptsExpiryAndCancelledConsent(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	p := oauthFixture(t, c, "google", false)
	bindOAuthAdmin(t, c, p, "google")
	_, _ = c.a.db.Exec("UPDATE users SET totp=? WHERE id=?", c.a.seal("JBSWY3DPEHPK3PXP"), uid)
	b := newOAuthBrowser(c, false)
	state := b.start(p, "google", "login")
	b.callback("google", state)
	for range 5 {
		b.json("POST", "/api/auth/oauth/complete", map[string]string{"code": "invalid"}, 401)
	}
	b.json("GET", "/api/auth/oauth/pending", nil, 401)
	b.json("POST", "/api/auth/oauth/cancel", map[string]string{}, 200)
	if b.cookies[oauthPendingCookie] != nil {
		t.Fatal("cancel did not clear ticket")
	}
	state = b.start(p, "google", "login")
	before := p.calls
	w := b.do("GET", "/api/auth/oauth/google/callback?error=access_denied&state="+state, nil, 303)
	requireOAuthReturn(t, w, "cancelled")
	if p.calls != before {
		t.Fatal("cancelled consent exchanged a token")
	}
}
func TestOAuthConfigurationPermissionsEncryptionAndInvalidation(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	p := oauthFixture(t, c, "google", true)
	var stored string
	_ = c.a.db.QueryRow("SELECT value FROM settings WHERE key='oauth.google'").Scan(&stored)
	if strings.Contains(stored, "provider-secret") || strings.Contains(stored, "client-id") {
		t.Fatal("unencrypted provider credentials")
	}
	admin := c.request("GET", "/api/admin/oauth", nil, 200)
	raw, _ := json.Marshal(admin)
	if strings.Contains(string(raw), "provider-secret") || strings.Contains(string(raw), "client_secret") {
		t.Fatal("provider secret returned by API")
	}
	b := newOAuthBrowser(c, false)
	state := b.start(p, "google", "login")
	cfg, _ := c.a.readOAuthConfig("google")
	in := map[string]any{"enabled": false, "client_id": "client-id", "client_secret": "", "allow_signup": false, "expected_revision": cfg.Revision, "password": "admin-password-123"}
	c.request("PUT", "/api/admin/oauth/google", in, 200)
	c.request("PUT", "/api/admin/oauth/google", in, 409)
	updated, _ := c.a.readOAuthConfig("google")
	if updated.ClientSecret != "provider-secret" {
		t.Fatal("blank secret did not preserve credential")
	}
	requireOAuthReturn(t, b.callback("google", state), "config_changed")
	if p.calls != 0 {
		t.Fatal("disabled provider exchanged token")
	}
	_, _ = c.a.db.Exec("UPDATE users SET role='user' WHERE id=?", uid)
	c.request("GET", "/api/admin/oauth", nil, 403)
	c.request("PUT", "/api/admin/oauth/google", in, 403)
	c.cookie = nil
	c.request("GET", "/api/admin/oauth", nil, 401)
}

func TestThirdOAuthAppCodeExchangeAndIsolation(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", false)
	bindOAuthAdmin(t, c, p, "google")
	aid := c.app("OAuth 应用")
	other := c.app("其他应用")
	key := c.makeKey(aid, "auth:login", "session:read")
	otherKey := c.makeKey(other, "auth:login", "session:read")
	redirect := "https://app.example.test/auth/callback"
	c.request("PUT", "/api/applications/"+aid+"/oauth", map[string]any{"redirect_uris": []string{redirect}}, 200)
	c.key = key
	verifier := strings.Repeat("v", 64)
	in := map[string]string{"redirect_uri": redirect, "state": "browser-session-state-123", "code_challenge": oauthChallenge(verifier), "code_challenge_method": "S256"}
	v := c.request("POST", "/v1/auth/oauth/google/start", in, 200)
	b := newOAuthBrowser(c, false)
	launch := b.do("GET", v["authorization_url"].(string), nil, 303)
	u, _ := url.Parse(launch.Header().Get("Location"))
	p.challenge = u.Query().Get("code_challenge")
	state := u.Query().Get("state")
	b.callback("google", state)
	if b.json("GET", "/api/auth/oauth/pending", nil, 200)["application"] != "OAuth 应用" {
		t.Fatal("missing application consent")
	}
	completed := b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
	target, _ := url.Parse(completed["redirect_url"].(string))
	grant := target.Query().Get("code")
	if target.Query().Get("state") != in["state"] || grant == "" || b.cookies["auth_session"] != nil {
		t.Fatal("application flow leaked console session or lost state")
	}
	exchange := map[string]string{"code": grant, "code_verifier": verifier, "redirect_uri": redirect}
	c.key = otherKey
	c.request("POST", "/v1/auth/oauth/exchange", exchange, 401)
	c.key = key
	invalid := map[string]string{"code": grant, "code_verifier": strings.Repeat("x", 64), "redirect_uri": redirect}
	c.request("POST", "/v1/auth/oauth/exchange", invalid, 401)
	invalid["code_verifier"] = verifier
	invalid["redirect_uri"] = "https://evil.example/callback"
	c.request("POST", "/v1/auth/oauth/exchange", invalid, 401)
	response := c.request("POST", "/v1/auth/oauth/exchange", exchange, 200)
	c.bearer = response["access_token"].(string)
	c.cookie = nil
	c.request("GET", "/v1/session", nil, 200)
	c.request("GET", "/api/me", nil, 401)
	c.request("POST", "/v1/auth/oauth/exchange", exchange, 401)
	c.key = otherKey
	c.request("GET", "/v1/session", nil, 401)
}

func TestThirdOAuthRejectsUnsafeRedirectAndRevokedKey(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", true)
	aid := c.app("OAuth")
	key := c.makeKey(aid, "auth:login")
	for _, uri := range []string{"http://app.example.test/cb", "javascript:alert(1)", "https://user:password@app.example.test/cb", "https://app.example.test/cb#fragment", "https://app.example.test/cb?code=bad"} {
		c.request("PUT", "/api/applications/"+aid+"/oauth", map[string]any{"redirect_uris": []string{uri}}, 400)
	}
	redirect := "http://127.0.0.1:9000/callback"
	c.request("PUT", "/api/applications/"+aid+"/oauth", map[string]any{"redirect_uris": []string{redirect}}, 200)
	c.key = key
	in := map[string]string{"redirect_uri": redirect, "state": "random-state-value-123", "code_challenge": oauthChallenge(strings.Repeat("v", 64)), "code_challenge_method": "S256"}
	start := c.request("POST", "/v1/auth/oauth/google/start", in, 200)
	_, _ = c.a.db.Exec("UPDATE api_keys SET revoked=1 WHERE hash=?", hash(key))
	b := newOAuthBrowser(c, false)
	requireOAuthReturn(t, b.do("GET", start["authorization_url"].(string), nil, 303), "invalid_state")
	if p.calls != 0 {
		t.Fatal("revoked key reached provider")
	}
	c.request("POST", "/v1/auth/oauth/google/start", in, 401)
}

func TestOAuthConcurrentCompletionMintsOneSession(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", false)
	bindOAuthAdmin(t, c, p, "google")
	b := newOAuthBrowser(c, false)
	state := b.start(p, "google", "login")
	b.callback("google", state)
	pending := b.cookies[oauthPendingCookie]
	handler := c.a.Handler()
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			r := httptest.NewRequest("POST", "/api/auth/oauth/complete", strings.NewReader(`{}`))
			r.AddCookie(pending)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			results <- w.Code
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for result := range results {
		if result == 200 {
			success++
		} else if result != 401 {
			t.Fatal("unexpected concurrent response", result)
		}
	}
	if success != 1 {
		t.Fatal("ticket used more than once", success)
	}
}

func TestOAuthMigrationFromSchemaTwoPreservesData(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	directory := c.a.cfg.DataDir
	_, err := c.a.db.Exec("INSERT INTO user_activity_daily VALUES(?, 'console', 0, 7, 1, 2)", uid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.a.db.Exec("DROP TABLE oauth_flows; DROP TABLE oauth_identities; DROP TABLE application_oauth; PRAGMA user_version=2;")
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, _ := os.ReadFile(filepath.Join(directory, "master.key"))
	c.a.Close()
	for range 2 {
		a, err := New(Config{DataDir: directory})
		if err != nil {
			t.Fatal(err)
		}
		var version, count int
		_ = a.db.QueryRow("PRAGMA user_version").Scan(&version)
		_ = a.db.QueryRow("SELECT SUM(logins) FROM user_activity_daily WHERE user_id=?", uid).Scan(&count)
		if version != SchemaVersion || count != 8 || !a.verifyPassword(uid, "admin-password-123") {
			t.Fatal("schema 2 upgrade modified existing data", version, count)
		}
		for _, table := range []string{"oauth_flows", "oauth_identities", "application_oauth"} {
			var n int
			if a.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n) != nil {
				t.Fatal("missing OAuth table", table)
			}
		}
		a.Close()
	}
	keyAfter, _ := os.ReadFile(filepath.Join(directory, "master.key"))
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("migration replaced master key")
	}
	// A higher structure version is rejected before migration touches any data.
	db, err := sql.Open("sqlite", filepath.Join(directory, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	_ = db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 3 {
		t.Fatal(version)
	}
}

func TestOAuthBindingTotpCannotReplayAtLogin(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	p := oauthFixture(t, c, "google", false)
	secret := "JBSWY3DPEHPK3PXP"
	_, err := c.a.db.Exec("UPDATE users SET totp=?,last_totp=0 WHERE id=?", c.a.seal(secret), uid)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := newOAuthBrowser(c, true)
	start := b.json("POST", "/api/auth/oauth/google/start", map[string]string{"intent": "bind", "password": "admin-password-123", "code": code}, 200)
	u, _ := url.Parse(start["authorization_url"].(string))
	p.challenge = u.Query().Get("code_challenge")
	b.json("POST", "/api/auth/oauth/google/start", map[string]string{"intent": "bind", "password": "admin-password-123", "code": code}, 403)
	b.callback("google", u.Query().Get("state"))
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
	login := newOAuthBrowser(c, false)
	state := login.start(p, "google", "login")
	login.callback("google", state)
	login.json("POST", "/api/auth/oauth/complete", map[string]string{"code": code}, 401)
}

func TestOAuthIdentityCannotBindAnotherUser(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", false)
	bindOAuthAdmin(t, c, p, "google")
	aid := c.app("注册")
	c.key = c.makeKey(aid, "auth:register")
	v := c.request("POST", "/v1/auth/register", map[string]string{"email": "other@example.test", "name": "Other", "password": "other-password-123"}, 201)
	otherID := v["user"].(map[string]any)["id"].(string)
	c.cookie = nil
	c.request("POST", "/api/auth/login", map[string]string{"email": "other@example.test", "password": "other-password-123"}, 200)
	b := newOAuthBrowser(c, true)
	started := b.json("POST", "/api/auth/oauth/google/start", map[string]string{"intent": "bind", "password": "other-password-123"}, 200)
	u, _ := url.Parse(started["authorization_url"].(string))
	p.challenge = u.Query().Get("code_challenge")
	b.callback("google", u.Query().Get("state"))
	b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 409)
	var count int
	_ = c.a.db.QueryRow("SELECT COUNT(*) FROM oauth_identities WHERE user_id=?", otherID).Scan(&count)
	if count != 0 {
		t.Fatal("provider identity linked to two accounts")
	}
	c.request("GET", "/api/admin/oauth", nil, 403)
	c.request("GET", "/api/applications/"+aid+"/oauth", nil, 403)
}

func TestThirdOAuthGrantInvalidatesOnPermissionAndIdentityChanges(t *testing.T) {
	for _, scenario := range []string{"key", "owner", "user", "redirect", "password", "identity", "config", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			c := newTest(t)
			uid := c.setup()
			p := oauthFixture(t, c, "google", false)
			bindOAuthAdmin(t, c, p, "google")
			aid := c.app("App")
			c.key = c.makeKey(aid, "auth:login")
			redirect := "https://app.example.test/callback"
			verifier := strings.Repeat("v", 64)
			c.request("PUT", "/api/applications/"+aid+"/oauth", map[string]any{"redirect_uris": []string{redirect}}, 200)
			started := c.request("POST", "/v1/auth/oauth/google/start", map[string]string{"redirect_uri": redirect, "state": "random-state-value-123", "code_challenge": oauthChallenge(verifier), "code_challenge_method": "S256"}, 200)
			b := newOAuthBrowser(c, false)
			launch := b.do("GET", started["authorization_url"].(string), nil, 303)
			u, _ := url.Parse(launch.Header().Get("Location"))
			p.challenge = u.Query().Get("code_challenge")
			b.callback("google", u.Query().Get("state"))
			completed := b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
			target, _ := url.Parse(completed["redirect_url"].(string))
			grant := target.Query().Get("code")
			var err error
			switch scenario {
			case "key":
				_, err = c.a.db.Exec("UPDATE api_keys SET revoked=1")
			case "owner":
				_, err = c.a.db.Exec("UPDATE users SET role='user' WHERE id=?", uid)
			case "user":
				_, err = c.a.db.Exec("UPDATE users SET disabled=1 WHERE id=?", uid)
			case "redirect":
				_, err = c.a.db.Exec("DELETE FROM application_oauth")
			case "password":
				_, err = c.a.db.Exec("UPDATE users SET password='changed' WHERE id=?", uid)
			case "identity":
				_, err = c.a.db.Exec("UPDATE oauth_identities SET binding_id=? WHERE user_id=?", id(), uid)
			case "config":
				cfg, _ := c.a.readOAuthConfig("google")
				cfg.Revision = token()
				raw, _ := json.Marshal(cfg)
				_, err = c.a.db.Exec("UPDATE settings SET value=? WHERE key='oauth.google'", c.a.seal(string(raw)))
			case "expired":
				_, err = c.a.db.Exec("UPDATE oauth_flows SET expires_at=0 WHERE phase='grant'")
			}
			if err != nil {
				t.Fatal(err)
			}
			c.request("POST", "/v1/auth/oauth/exchange", map[string]string{"code": grant, "code_verifier": verifier, "redirect_uri": redirect}, 401)
			var sessions int
			_ = c.a.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE app_id=?", aid).Scan(&sessions)
			if sessions != 0 {
				t.Fatal("revoked permission still minted application session")
			}
		})
	}
}

func TestThirdOAuthRegistrationRequiresRegisterScope(t *testing.T) {
	c := newTest(t)
	c.setup()
	p := oauthFixture(t, c, "google", true)
	p.email = "new-app-user@example.test"
	aid := c.app("App")
	c.key = c.makeKey(aid, "auth:login")
	redirect := "https://app.example.test/callback"
	c.request("PUT", "/api/applications/"+aid+"/oauth", map[string]any{"redirect_uris": []string{redirect}}, 200)
	started := c.request("POST", "/v1/auth/oauth/google/start", map[string]string{"redirect_uri": redirect, "state": "browser-state-random-123", "code_challenge": oauthChallenge(strings.Repeat("v", 64)), "code_challenge_method": "S256"}, 200)
	b := newOAuthBrowser(c, false)
	launch := b.do("GET", started["authorization_url"].(string), nil, 303)
	u, _ := url.Parse(launch.Header().Get("Location"))
	p.challenge = u.Query().Get("code_challenge")
	requireOAuthReturn(t, b.callback("google", u.Query().Get("state")), "registration_disabled")
	var count int
	_ = c.a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 1 {
		t.Fatal("login-only Key registered a user")
	}
}

func TestOAuthOriginAndBearerIsolation(t *testing.T) {
	c := newTest(t)
	c.setup()
	oauthFixture(t, c, "google", false)
	aid := c.app("App")
	c.key = c.makeKey(aid, "auth:login")
	login := c.request("POST", "/v1/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123"}, 200)
	c.bearer = login["access_token"].(string)
	c.cookie = nil
	c.request("POST", "/api/auth/oauth/google/start", map[string]string{"intent": "bind", "password": "admin-password-123"}, 401)
	c.request("GET", "/api/account/identities", nil, 401)
	c.request("GET", "/api/admin/oauth", nil, 401)
	for _, path := range []string{"/api/auth/oauth/google/start", "/api/auth/oauth/complete", "/api/account/identities/google/unlink"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		c.a.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("OAuth write accepted cross-site Origin", path, w.Code)
		}
	}
}

func TestOAuthBindingsAndInFlightStateSurviveRestart(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	p := oauthFixture(t, c, "github", false)
	bindOAuthAdmin(t, c, p, "github")
	b := newOAuthBrowser(c, false)
	state := b.start(p, "github", "login")
	directory := c.a.cfg.DataDir
	transport := c.a.oauthClient
	c.a.Close()
	a, err := New(Config{DataDir: directory, PublicURL: "http://auth.test", WebDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.oauthClient = transport
	c.a = a
	cfg, err := a.readOAuthConfig("github")
	if err != nil || cfg.ClientSecret != "provider-secret" || !cfg.Enabled {
		t.Fatal("encrypted configuration not retained", err)
	}
	requireOAuthReturn(t, b.callback("github", state), "oauth=complete")
	result := b.json("POST", "/api/auth/oauth/complete", map[string]string{}, 200)
	if result["user"].(map[string]any)["id"] != uid {
		t.Fatal("restart lost identity binding")
	}
}
