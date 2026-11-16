package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Provider endpoints are fixed, never supplied by the browser or administrator.
type oauthProvider struct{ authorize, token, profile, scope string }

var oauthProviders = map[string]oauthProvider{
	"google": {"https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", "https://openidconnect.googleapis.com/v1/userinfo", "openid email profile"},
	"github": {"https://github.com/login/oauth/authorize", "https://github.com/login/oauth/access_token", "https://api.github.com/user", "read:user user:email"},
}

type oauthConfig struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	AllowSignup  bool   `json:"allow_signup"`
	Revision     string `json:"revision"`
}

// Flow payloads include PKCE verifiers and security snapshots, so are encrypted.
// Only digests of state, tickets and browser credentials are stored in columns.
type oauthFlow struct {
	Intent      string `json:"intent"`
	ConfigHash  string `json:"config_hash"`
	Verifier    string `json:"verifier,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Security    string `json:"security,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Email       string `json:"email,omitempty"`
	Name        string `json:"name,omitempty"`
	AppID       string `json:"app_id,omitempty"`
	KeyHash     string `json:"key_hash,omitempty"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	ClientState string `json:"client_state,omitempty"`
	Challenge   string `json:"challenge,omitempty"`
}

func (a *App) readOAuthConfig(provider string) (oauthConfig, error) {
	return a.readOAuthConfigFrom(a.db, provider)
}
func (a *App) readOAuthConfigFrom(db queryRower, provider string) (oauthConfig, error) {
	var enc string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", "oauth."+provider).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return oauthConfig{}, nil
	}
	if err != nil {
		return oauthConfig{}, err
	}
	raw, err := a.open(enc)
	if err != nil {
		return oauthConfig{}, err
	}
	var cfg oauthConfig
	err = json.Unmarshal([]byte(raw), &cfg)
	return cfg, err
}

func oauthConfigHash(cfg oauthConfig) string { b, _ := json.Marshal(cfg); return hash(string(b)) }
func (a *App) oauthCallbackURL(provider string) string {
	return strings.TrimRight(a.cfg.PublicURL, "/") + "/api/auth/oauth/" + provider + "/callback"
}
func (a *App) oauthPublic(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, provider := range []string{"google", "github"} {
		cfg, err := a.readOAuthConfig(provider)
		if err != nil {
			fail(w, 500, "读取第三方登录配置失败")
			return
		}
		if cfg.Enabled {
			out = append(out, map[string]any{"provider": provider, "allow_signup": cfg.AllowSignup})
		}
	}
	jsonOut(w, 200, map[string]any{"providers": out})
}
func (a *App) oauthAdmin(w http.ResponseWriter, r *http.Request, s *session) {
	out := []map[string]any{}
	for _, provider := range []string{"google", "github"} {
		cfg, err := a.readOAuthConfig(provider)
		if err != nil {
			fail(w, 500, "读取配置失败")
			return
		}
		out = append(out, map[string]any{"provider": provider, "enabled": cfg.Enabled, "client_id": cfg.ClientID, "has_secret": cfg.ClientSecret != "", "allow_signup": cfg.AllowSignup, "revision": cfg.Revision, "callback_url": a.oauthCallbackURL(provider)})
	}
	jsonOut(w, 200, map[string]any{"providers": out})
}
func (a *App) oauthReauthenticate(w http.ResponseWriter, s *session, password, code string) bool {
	if !a.verifyPassword(s.UserID, password) || s.User.TFA && !a.verifyCode(s.UserID, code) {
		fail(w, 403, "密码或两步验证码不正确")
		return false
	}
	return true
}
func (a *App) oauthSave(w http.ResponseWriter, r *http.Request, s *session) {
	provider := r.PathValue("provider")
	if _, ok := oauthProviders[provider]; !ok {
		fail(w, 404, "不支持的登录服务")
		return
	}
	var in struct {
		Enabled      bool   `json:"enabled"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AllowSignup  bool   `json:"allow_signup"`
		Revision     string `json:"expected_revision"`
		Password     string `json:"password"`
		Code         string `json:"code"`
	}
	if !decode(w, r, &in) || !a.oauthReauthenticate(w, s, in.Password, in.Code) {
		return
	}
	old, err := a.readOAuthConfig(provider)
	if err != nil {
		fail(w, 500, "读取配置失败")
		return
	}
	if old.Revision != in.Revision {
		fail(w, 409, "配置已变更，请刷新后重试")
		return
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	if len(in.ClientID) > 500 || len(in.ClientSecret) > 2000 || strings.ContainsAny(in.ClientID+in.ClientSecret, "\r\n\x00") {
		fail(w, 400, "Client ID 或 Secret 格式不正确")
		return
	}
	if in.ClientSecret == "" {
		in.ClientSecret = old.ClientSecret
	}
	if in.ClientID != old.ClientID && in.ClientSecret == old.ClientSecret && old.ClientID != "" {
		fail(w, 400, "变更 Client ID 时请同时填写新的 Client Secret")
		return
	}
	if in.Enabled && (in.ClientID == "" || in.ClientSecret == "") {
		fail(w, 400, "启用前请填写 Client ID 和 Client Secret")
		return
	}
	cfg := oauthConfig{in.Enabled, in.ClientID, in.ClientSecret, in.AllowSignup, token()}
	b, _ := json.Marshal(cfg)
	// Compare the encrypted prior value too, so concurrent administrator edits
	// cannot silently overwrite each other after password verification.
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	var enc string
	err = tx.QueryRow("SELECT value FROM settings WHERE key=?", "oauth."+provider).Scan(&enc)
	if err == nil {
		raw, e := a.open(enc)
		var current oauthConfig
		if e != nil || json.Unmarshal([]byte(raw), &current) != nil || current.Revision != old.Revision {
			fail(w, 409, "配置已变更，请刷新后重试")
			return
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		fail(w, 500, "读取配置失败")
		return
	}
	_, err = tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "oauth."+provider, a.seal(string(b)))
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "保存失败")
		return
	}
	a.audit(s.UserID, "oauth.configure", provider, ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}

func (a *App) oauthSecurity(uid string) (string, error) {
	return oauthSecurityFrom(a.db, uid)
}
func oauthSecurityFrom(db queryRower, uid string) (string, error) {
	var password, totp, bindings string
	// Binding nonces also invalidate tickets when an identity is unlinked and
	// subsequently linked again to the same user within the ticket lifetime.
	err := db.QueryRow(`SELECT password,totp,COALESCE((SELECT GROUP_CONCAT(binding_id,',') FROM
 (SELECT binding_id FROM oauth_identities WHERE user_id=? ORDER BY provider)),'')
 FROM users WHERE id=? AND disabled=0`, uid, uid).Scan(&password, &totp, &bindings)
	return hash(password + ":" + totp + ":" + bindings), err
}
func oauthChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

var pkcePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func (a *App) saveOAuthFlow(raw, provider, phase, browser string, f oauthFlow, ttl int64) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = a.db.Exec("DELETE FROM oauth_flows WHERE expires_at<=?", now())
	if err != nil {
		return err
	}
	_, err = a.db.Exec("INSERT INTO oauth_flows(hash,provider,phase,browser_hash,payload,expires_at) VALUES(?,?,?,?,?,?)", hash(raw), provider, phase, browser, a.seal(string(b)), now()+ttl)
	return err
}
func (a *App) readOAuthFlow(raw, phase string) (oauthFlow, string, string, error) {
	var enc, provider, browser string
	err := a.db.QueryRow("SELECT provider,browser_hash,payload FROM oauth_flows WHERE hash=? AND phase=? AND expires_at>? AND attempts<5", hash(raw), phase, now()).Scan(&provider, &browser, &enc)
	if err != nil {
		return oauthFlow{}, "", "", err
	}
	v, err := a.open(enc)
	if err != nil {
		return oauthFlow{}, "", "", err
	}
	var f oauthFlow
	err = json.Unmarshal([]byte(v), &f)
	return f, provider, browser, err
}
func (a *App) consumeOAuthFlow(raw, phase string) bool {
	res, err := a.db.Exec("DELETE FROM oauth_flows WHERE hash=? AND phase=? AND expires_at>? AND attempts<5", hash(raw), phase, now())
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}
func (a *App) oauthCookie(w http.ResponseWriter, name, value, path string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: age, HttpOnly: true, Secure: a.cfg.SecureCookie, SameSite: http.SameSiteLaxMode})
}

const oauthPendingCookie = "auth_oauth_pending"

func (a *App) oauthAuthorization(w http.ResponseWriter, r *http.Request, provider string, cfg oauthConfig, f oauthFlow) string {
	raw, browser := token(), token()
	f.Verifier = token()
	f.ConfigHash = oauthConfigHash(cfg)
	if a.saveOAuthFlow(raw, provider, "authorize", hash(browser), f, 600) != nil {
		return ""
	}
	a.oauthCookie(w, "auth_oauth_"+hash(raw)[:16], browser, "/", 600)
	u, _ := url.Parse(oauthProviders[provider].authorize)
	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", a.oauthCallbackURL(provider))
	q.Set("response_type", "code")
	q.Set("scope", oauthProviders[provider].scope)
	q.Set("state", raw)
	q.Set("code_challenge", oauthChallenge(f.Verifier))
	q.Set("code_challenge_method", "S256")
	if provider == "google" {
		q.Set("prompt", "select_account")
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func (a *App) oauthStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if _, ok := oauthProviders[provider]; !ok {
		fail(w, 404, "不支持的登录服务")
		return
	}
	cfg, err := a.readOAuthConfig(provider)
	if err != nil || !cfg.Enabled {
		fail(w, 409, "此登录服务尚未启用")
		return
	}
	var in struct {
		Intent   string `json:"intent"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	f := oauthFlow{Intent: in.Intent}
	if f.Intent == "" {
		f.Intent = "login"
	}
	if f.Intent == "bind" {
		s, e := a.session(r, true)
		if e != nil {
			fail(w, 401, "请先登录要绑定的账户")
			return
		}
		if !a.oauthReauthenticate(w, s, in.Password, in.Code) {
			return
		}
		f.UserID = s.UserID
		f.SessionID = s.ID
		f.Security, err = a.oauthSecurity(s.UserID)
		if err != nil {
			fail(w, 403, "账户不可用")
			return
		}
	} else if f.Intent != "login" {
		fail(w, 400, "登录意图无效")
		return
	}
	authorize := a.oauthAuthorization(w, r, provider, cfg, f)
	if authorize == "" {
		fail(w, 500, "无法开始授权")
		return
	}
	jsonOut(w, 200, map[string]string{"authorization_url": authorize})
}

func (a *App) oauthJSON(ctx context.Context, method, endpoint, bearer string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", "Auth-Center")
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := a.oauthClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(r)
	if err != nil {
		return errors.New("第三方服务暂不可用")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("第三方授权失败")
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if decoder.Decode(out) != nil {
		return errors.New("第三方响应无效")
	}
	return nil
}
func (a *App) oauthIdentity(ctx context.Context, provider string, cfg oauthConfig, code, verifier string) (subject, email, name string, err error) {
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	err = a.oauthJSON(ctx, "POST", oauthProviders[provider].token, "", url.Values{"client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "code": {code}, "grant_type": {"authorization_code"}, "redirect_uri": {a.oauthCallbackURL(provider)}, "code_verifier": {verifier}}, &result)
	if err != nil || result.Error != "" || result.AccessToken == "" || !strings.EqualFold(result.TokenType, "bearer") {
		return "", "", "", errors.New("第三方授权失败")
	}
	if provider == "google" {
		var profile struct {
			Sub      string `json:"sub"`
			Email    string `json:"email"`
			Verified bool   `json:"email_verified"`
			Name     string `json:"name"`
		}
		err = a.oauthJSON(ctx, "GET", oauthProviders[provider].profile, result.AccessToken, nil, &profile)
		if err != nil || !profile.Verified {
			return "", "", "", errors.New("需要已验证的邮箱")
		}
		subject, email, name = profile.Sub, profile.Email, profile.Name
	} else {
		var profile struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Name  string `json:"name"`
		}
		err = a.oauthJSON(ctx, "GET", oauthProviders[provider].profile, result.AccessToken, nil, &profile)
		if err != nil || profile.ID <= 0 {
			return "", "", "", errors.New("无法读取 GitHub 身份")
		}
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		err = a.oauthJSON(ctx, "GET", "https://api.github.com/user/emails", result.AccessToken, nil, &emails)
		if err != nil {
			return "", "", "", err
		}
		for _, v := range emails {
			if v.Primary && v.Verified {
				email = v.Email
				break
			}
		}
		subject = strconv.FormatInt(profile.ID, 10)
		name = profile.Name
		if name == "" {
			name = profile.Login
		}
	}
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if subject == "" || len(subject) > 255 || !emailValid(email) {
		return "", "", "", errors.New("需要有效且已验证的邮箱")
	}
	if !validText(name, 80) {
		name = strings.Split(email, "@")[0]
		if len(name) > 80 {
			name = "新用户"
		}
	}
	return subject, email, name, nil
}
func (a *App) oauthFailure(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, strings.TrimRight(a.cfg.PublicURL, "/")+"/?oauth_error="+url.QueryEscape(code), http.StatusSeeOther)
}
func (a *App) oauthCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	raw := r.URL.Query().Get("state")
	f, p, browser, err := a.readOAuthFlow(raw, "authorize")
	cookie, cookieErr := r.Cookie("auth_oauth_" + hash(raw)[:16])
	if err != nil || p != provider || cookieErr != nil || hash(cookie.Value) != browser {
		a.oauthFailure(w, r, "invalid_state")
		return
	}
	if !a.consumeOAuthFlow(raw, "authorize") {
		a.oauthFailure(w, r, "invalid_state")
		return
	}
	a.oauthCookie(w, cookie.Name, "", "/", -1)
	cfg, err := a.readOAuthConfig(provider)
	if err != nil || !cfg.Enabled || oauthConfigHash(cfg) != f.ConfigHash {
		a.oauthFailure(w, r, "config_changed")
		return
	}
	if r.URL.Query().Get("error") != "" {
		a.oauthFailure(w, r, "cancelled")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 {
		a.oauthFailure(w, r, "provider_failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	f.Subject, f.Email, f.Name, err = a.oauthIdentity(ctx, provider, cfg, code, f.Verifier)
	f.Verifier = ""
	if err != nil {
		a.oauthFailure(w, r, "provider_failed")
		return
	}
	if f.Intent == "login" {
		var uid string
		err = a.db.QueryRow("SELECT user_id FROM oauth_identities WHERE provider=? AND subject=?", provider, f.Subject).Scan(&uid)
		if err == nil {
			f.UserID = uid
			f.Security, err = a.oauthSecurity(uid)
			if err != nil {
				a.oauthFailure(w, r, "unavailable")
				return
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			var count int
			if a.db.QueryRow("SELECT COUNT(*) FROM users WHERE email=?", f.Email).Scan(&count) != nil {
				a.oauthFailure(w, r, "provider_failed")
				return
			}
			if count > 0 {
				a.oauthFailure(w, r, "binding_required")
				return
			}
			if !cfg.AllowSignup || f.AppID != "" && !a.oauthAppAllowed(f, true) {
				a.oauthFailure(w, r, "registration_disabled")
				return
			}
		} else {
			a.oauthFailure(w, r, "provider_failed")
			return
		}
	}
	pending := token()
	if a.saveOAuthFlow(pending, provider, "pending", "", f, 300) != nil {
		a.oauthFailure(w, r, "provider_failed")
		return
	}
	a.oauthCookie(w, oauthPendingCookie, pending, "/api/auth/oauth", 300)
	http.Redirect(w, r, strings.TrimRight(a.cfg.PublicURL, "/")+"/?oauth=complete", http.StatusSeeOther)
}

func (a *App) oauthPending(r *http.Request) (oauthFlow, string, string, error) {
	c, err := r.Cookie(oauthPendingCookie)
	if err != nil {
		return oauthFlow{}, "", "", err
	}
	f, provider, _, err := a.readOAuthFlow(c.Value, "pending")
	if err != nil {
		return f, provider, c.Value, err
	}
	cfg, err := a.readOAuthConfig(provider)
	if err != nil || !cfg.Enabled || oauthConfigHash(cfg) != f.ConfigHash {
		return f, provider, c.Value, errors.New("configuration changed")
	}
	if f.AppID != "" && !a.oauthAppAllowed(f, false) {
		return f, provider, c.Value, errors.New("application unavailable")
	}
	if f.UserID != "" {
		security, e := a.oauthSecurity(f.UserID)
		if e != nil || security != f.Security {
			return f, provider, c.Value, errors.New("account changed")
		}
		if f.Intent == "bind" {
			s, e := a.session(r, true)
			if e != nil || s.ID != f.SessionID || s.UserID != f.UserID {
				return f, provider, c.Value, errors.New("session changed")
			}
		} else {
			var uid string
			if a.db.QueryRow("SELECT user_id FROM oauth_identities WHERE provider=? AND subject=?", provider, f.Subject).Scan(&uid) != nil || uid != f.UserID {
				return f, provider, c.Value, errors.New("identity changed")
			}
		}
	}
	return f, provider, c.Value, nil
}
func (a *App) oauthPendingStatus(w http.ResponseWriter, r *http.Request) {
	f, provider, _, err := a.oauthPending(r)
	if err != nil {
		fail(w, 401, "授权已失效，请重新开始登录或绑定")
		return
	}
	tfa := false
	if f.UserID != "" {
		u, e := a.user(f.UserID)
		if e != nil {
			fail(w, 401, "账户不可用")
			return
		}
		tfa = u.TFA && f.Intent == "login"
	}
	appName := ""
	if f.AppID != "" {
		if a.db.QueryRow("SELECT name FROM applications WHERE id=?", f.AppID).Scan(&appName) != nil {
			fail(w, 401, "应用不可用")
			return
		}
	}
	jsonOut(w, 200, map[string]any{"provider": provider, "intent": f.Intent, "email": f.Email, "name": f.Name, "requires_2fa": tfa, "requires_registration": f.UserID == "", "application": appName})
}
func (a *App) oauthComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	f, provider, raw, err := a.oauthPending(r)
	if err != nil {
		fail(w, 401, "授权已失效，请重新开始登录或绑定")
		return
	}
	res, err := a.db.Exec("UPDATE oauth_flows SET attempts=attempts+1 WHERE hash=? AND phase='pending' AND attempts<5 AND expires_at>?", hash(raw), now())
	if err != nil {
		fail(w, 500, "无法完成授权")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 401, "授权已失效")
		return
	}
	// A fifth attempt may succeed, but no sixth request can read this ticket.
	if f.UserID != "" && f.Intent == "login" {
		u, e := a.user(f.UserID)
		if e != nil || u.Disabled {
			fail(w, 403, "账户不可用")
			return
		}
		if u.TFA && !a.verifyCode(u.ID, in.Code) {
			jsonOut(w, 401, map[string]any{"error": "请输入未使用的两步验证码或备用码", "requires_2fa": true})
			return
		}
	}
	if f.UserID == "" {
		if checkPassword(in.Password) != nil || !validText(in.Name, 80) {
			fail(w, 400, "请设置姓名及 10–72 字节的本地密码")
			return
		}
		if f.AppID != "" && !a.oauthAppAllowed(f, true) {
			fail(w, 403, "应用没有注册权限")
			return
		}
	}
	// Consume atomically before writing an identity or minting a session/grant.
	res, err = a.db.Exec("DELETE FROM oauth_flows WHERE hash=? AND phase='pending' AND expires_at>?", hash(raw), now())
	if err != nil {
		fail(w, 500, "无法完成授权")
		return
	}
	n, _ = res.RowsAffected()
	if n != 1 {
		fail(w, 401, "授权已使用")
		return
	}
	a.oauthCookie(w, oauthPendingCookie, "", "/api/auth/oauth", -1)
	registering := f.UserID == ""
	if registering || f.Intent == "bind" {
		tx, e := a.db.Begin()
		if e != nil {
			fail(w, 500, "保存账户失败")
			return
		}
		defer tx.Rollback()
		if !a.oauthAccountValid(tx, provider, f) {
			fail(w, 401, "授权状态已变更，请重新开始")
			return
		}
		if f.UserID == "" {
			f.UserID = id()
			digest, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
			if e != nil {
				fail(w, 500, "创建账户失败")
				return
			}
			_, e = tx.Exec("INSERT INTO users(id,email,name,password,role,created_at) VALUES(?,?,?,?,'user',?)", f.UserID, f.Email, strings.TrimSpace(in.Name), string(digest), now())
			if e != nil {
				fail(w, 409, "邮箱已注册，请使用已有账户登录后绑定")
				return
			}
		}
		_, e = tx.Exec("INSERT INTO oauth_identities(provider,subject,user_id,email,created_at,binding_id) VALUES(?,?,?,?,?,?)", provider, f.Subject, f.UserID, f.Email, now(), id())
		if e != nil {
			fail(w, 409, "此第三方身份已绑定，或账户已绑定此服务；请先解绑再重试")
			return
		}
		if tx.Commit() != nil {
			fail(w, 500, "保存绑定失败")
			return
		}
		a.audit(f.UserID, "oauth.bind", provider, ip(r))
		if f.Intent == "bind" {
			jsonOut(w, 200, map[string]bool{"bound": true})
			return
		}
		a.audit(f.UserID, "auth.register", "oauth:"+provider, ip(r))
	}
	u, err := a.user(f.UserID)
	if err != nil || u.Disabled {
		fail(w, 403, "账户不可用")
		return
	}
	if registering {
		f.Security, err = a.oauthSecurity(u.ID)
		if err != nil {
			fail(w, 403, "账户不可用")
			return
		}
	}
	if f.AppID != "" {
		grant := token()
		if a.saveOAuthFlow(grant, provider, "grant", "", f, 60) != nil {
			fail(w, 500, "创建授权码失败")
			return
		}
		target, _ := url.Parse(f.RedirectURI)
		q := target.Query()
		q.Set("code", grant)
		q.Set("state", f.ClientState)
		target.RawQuery = q.Encode()
		jsonOut(w, 200, map[string]string{"redirect_url": target.String()})
		return
	}
	a.finishLoginGuarded(w, r, u, "console", true, func(tx *sql.Tx) bool { return a.oauthAccountValid(tx, provider, f) })
}

func (a *App) oauthIdentities(w http.ResponseWriter, r *http.Request, s *session) {
	rows, err := a.db.Query("SELECT provider,email,created_at FROM oauth_identities WHERE user_id=? ORDER BY provider", s.UserID)
	if err != nil {
		fail(w, 500, "读取绑定失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var provider, email string
		var created int64
		if rows.Scan(&provider, &email, &created) != nil {
			fail(w, 500, "读取绑定失败")
			return
		}
		out = append(out, map[string]any{"provider": provider, "email": email, "created_at": created})
	}
	if rows.Err() != nil {
		fail(w, 500, "读取绑定失败")
		return
	}
	jsonOut(w, 200, map[string]any{"identities": out})
}
func (a *App) oauthUnlink(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &in) || !a.oauthReauthenticate(w, s, in.Password, in.Code) {
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "解绑失败")
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM oauth_identities WHERE user_id=? AND provider=?", s.UserID, r.PathValue("provider"))
	if err != nil {
		fail(w, 500, "解绑失败")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 404, "未绑定此服务")
		return
	}
	_, err = tx.Exec("DELETE FROM sessions WHERE user_id=? AND id<>?", s.UserID, s.ID)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "解绑失败")
		return
	}
	a.audit(s.UserID, "oauth.unbind", r.PathValue("provider"), ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}

func validOAuthRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 2000 || strings.ContainsAny(raw, "\r\n\x00") || strings.ContainsAny(u.Host, "*\\") || u.Opaque != "" {
		return false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "[::1]" || u.Hostname() == "::1")) {
		return false
	}
	q := u.Query()
	return !q.Has("code") && !q.Has("state") && !q.Has("error")
}
func (a *App) oauthRedirects(appID string) ([]string, error) {
	return oauthRedirectsFrom(a.db, appID)
}
func oauthRedirectsFrom(db queryRower, appID string) ([]string, error) {
	var raw string
	err := db.QueryRow("SELECT redirect_uris FROM application_oauth WHERE app_id=?", appID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var uris []string
	err = json.Unmarshal([]byte(raw), &uris)
	return uris, err
}
func (a *App) applicationOAuth(w http.ResponseWriter, r *http.Request, s *session) {
	aid := r.PathValue("id")
	if !a.ownsApp(s.UserID, s.User.Role, aid) {
		fail(w, 403, "没有此应用的管理权限")
		return
	}
	if r.Method == "GET" {
		uris, err := a.oauthRedirects(aid)
		if err != nil {
			fail(w, 500, "读取回调配置失败")
			return
		}
		jsonOut(w, 200, map[string]any{"redirect_uris": uris})
		return
	}
	var in struct {
		URIs []string `json:"redirect_uris"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.URIs) > 10 {
		fail(w, 400, "最多配置 10 个回调地址")
		return
	}
	seen := map[string]bool{}
	for _, uri := range in.URIs {
		if !validOAuthRedirect(uri) || seen[uri] {
			fail(w, 400, "回调地址必须为精确 HTTPS 地址且不能重复，本机开发允许 HTTP")
			return
		}
		seen[uri] = true
	}
	if in.URIs == nil {
		in.URIs = []string{}
	}
	raw, _ := json.Marshal(in.URIs)
	_, err := a.db.Exec("INSERT INTO application_oauth(app_id,redirect_uris) VALUES(?,?) ON CONFLICT(app_id) DO UPDATE SET redirect_uris=excluded.redirect_uris", aid, string(raw))
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	a.audit(s.UserID, "oauth.redirects", aid, ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) oauthAppAllowed(f oauthFlow, registration bool) bool {
	return oauthAppAllowedFrom(a.db, f, registration)
}
func oauthAppAllowedFrom(db queryRower, f oauthFlow, registration bool) bool {
	var scopes, role string
	var disabled bool
	err := db.QueryRow("SELECT k.scopes,u.role,u.disabled FROM api_keys k JOIN applications a ON a.id=k.app_id JOIN users u ON u.id=a.owner_id WHERE k.hash=? AND k.app_id=? AND k.revoked=0", f.KeyHash, f.AppID).Scan(&scopes, &role, &disabled)
	if err != nil || disabled || role != "admin" && role != "developer" || !slices.Contains(strings.Split(scopes, ","), "auth:login") || registration && !slices.Contains(strings.Split(scopes, ","), "auth:register") {
		return false
	}
	uris, err := oauthRedirectsFrom(db, f.AppID)
	return err == nil && slices.Contains(uris, f.RedirectURI)
}
func (a *App) oauthAccountValid(db queryRower, provider string, f oauthFlow) bool {
	cfg, err := a.readOAuthConfigFrom(db, provider)
	if err != nil || !cfg.Enabled || oauthConfigHash(cfg) != f.ConfigHash || f.AppID != "" && !oauthAppAllowedFrom(db, f, f.UserID == "") {
		return false
	}
	if f.UserID == "" {
		return cfg.AllowSignup
	}
	security, err := oauthSecurityFrom(db, f.UserID)
	if err != nil || security != f.Security {
		return false
	}
	if f.Intent == "bind" {
		var uid string
		return db.QueryRow("SELECT user_id FROM sessions WHERE id=? AND app_id='console' AND expires_at>?", f.SessionID, now()).Scan(&uid) == nil && uid == f.UserID
	}
	var uid string
	return db.QueryRow("SELECT user_id FROM oauth_identities WHERE provider=? AND subject=?", provider, f.Subject).Scan(&uid) == nil && uid == f.UserID
}
func (a *App) thirdOAuthStart(w http.ResponseWriter, r *http.Request) {
	aid, ok := a.apiKey(w, r, "auth:login")
	if !ok {
		return
	}
	provider := r.PathValue("provider")
	if _, ok := oauthProviders[provider]; !ok {
		fail(w, 404, "不支持的登录服务")
		return
	}
	var in struct {
		RedirectURI string `json:"redirect_uri"`
		State       string `json:"state"`
		Challenge   string `json:"code_challenge"`
		Method      string `json:"code_challenge_method"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !pkcePattern.MatchString(in.Challenge) || in.Method != "S256" || len(in.State) < 16 || len(in.State) > 256 {
		fail(w, 400, "需要 S256 PKCE 和 16–256 字节的随机 state")
		return
	}
	f := oauthFlow{Intent: "login", AppID: aid, KeyHash: hash(r.Header.Get("X-API-Key")), RedirectURI: in.RedirectURI, ClientState: in.State, Challenge: in.Challenge}
	if !a.oauthAppAllowed(f, false) {
		fail(w, 403, "回调地址未登记或应用无权登录")
		return
	}
	cfg, err := a.readOAuthConfig(provider)
	if err != nil || !cfg.Enabled {
		fail(w, 409, "此登录服务尚未启用")
		return
	}
	f.ConfigHash = oauthConfigHash(cfg)
	launch := token()
	if a.saveOAuthFlow(launch, provider, "launch", "", f, 300) != nil {
		fail(w, 500, "无法开始授权")
		return
	}
	jsonOut(w, 200, map[string]any{"authorization_url": strings.TrimRight(a.cfg.PublicURL, "/") + "/api/auth/oauth/launch?request=" + url.QueryEscape(launch), "expires_in": 300})
}
func (a *App) oauthLaunch(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("request")
	f, provider, _, err := a.readOAuthFlow(raw, "launch")
	if err != nil || !a.oauthAppAllowed(f, false) {
		a.oauthFailure(w, r, "invalid_state")
		return
	}
	cfg, err := a.readOAuthConfig(provider)
	if err != nil || !cfg.Enabled || oauthConfigHash(cfg) != f.ConfigHash || !a.consumeOAuthFlow(raw, "launch") {
		a.oauthFailure(w, r, "invalid_state")
		return
	}
	target := a.oauthAuthorization(w, r, provider, cfg, f)
	if target == "" {
		a.oauthFailure(w, r, "provider_failed")
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
func (a *App) thirdOAuthExchange(w http.ResponseWriter, r *http.Request) {
	aid, ok := a.apiKey(w, r, "auth:login")
	if !ok {
		return
	}
	var in struct {
		Code        string `json:"code"`
		Verifier    string `json:"code_verifier"`
		RedirectURI string `json:"redirect_uri"`
	}
	if !decode(w, r, &in) {
		return
	}
	f, provider, _, err := a.readOAuthFlow(in.Code, "grant")
	if err != nil || f.AppID != aid || f.KeyHash != hash(r.Header.Get("X-API-Key")) || f.RedirectURI != in.RedirectURI || !verifierPattern.MatchString(in.Verifier) || oauthChallenge(in.Verifier) != f.Challenge || !a.oauthAppAllowed(f, false) {
		fail(w, 401, "授权码无效、已过期或不属于此应用")
		return
	}
	cfg, err := a.readOAuthConfig(provider)
	security, e := a.oauthSecurity(f.UserID)
	var uid string
	identityErr := a.db.QueryRow("SELECT user_id FROM oauth_identities WHERE provider=? AND subject=?", provider, f.Subject).Scan(&uid)
	if err != nil || !cfg.Enabled || oauthConfigHash(cfg) != f.ConfigHash || e != nil || security != f.Security || identityErr != nil || uid != f.UserID || !a.consumeOAuthFlow(in.Code, "grant") {
		fail(w, 401, "授权已失效，请重新登录")
		return
	}
	u, err := a.user(f.UserID)
	if err != nil || u.Disabled {
		fail(w, 403, "账户不可用")
		return
	}
	a.finishLoginGuarded(w, r, u, aid, false, func(tx *sql.Tx) bool { return a.oauthAccountValid(tx, provider, f) })
}

func (a *App) oauthCancel(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(oauthPendingCookie); err == nil {
		_, _ = a.db.Exec("DELETE FROM oauth_flows WHERE hash=? AND phase='pending'", hash(c.Value))
	}
	a.oauthCookie(w, oauthPendingCookie, "", "/api/auth/oauth", -1)
	jsonOut(w, 200, map[string]bool{"ok": true})
}
