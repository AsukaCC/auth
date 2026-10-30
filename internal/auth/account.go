package auth

import (
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strings"
	"time"
)

func (a *App) status(w http.ResponseWriter, r *http.Request) {
	m, _ := a.mailConfig()
	jsonOut(w, 200, map[string]any{"mail_enabled": m.Enabled, "version": Version, "schema_version": SchemaVersion})
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-comparison-password"), bcrypt.DefaultCost)

type loginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (a *App) credentials(w http.ResponseWriter, in loginInput) (User, bool) {
	var uid, p string
	err := a.db.QueryRow("SELECT id,password FROM users WHERE email=?", strings.TrimSpace(in.Email)).Scan(&uid, &p)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
		fail(w, 401, "邮箱或密码不正确")
		return User{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(p), []byte(in.Password)) != nil {
		fail(w, 401, "邮箱或密码不正确")
		return User{}, false
	}
	u, e := a.user(uid)
	if e != nil || u.Disabled {
		fail(w, 403, "账户不可用，请联系管理员")
		return User{}, false
	}
	if u.TFA {
		if in.Code == "" {
			jsonOut(w, 401, map[string]any{"error": "请输入验证器验证码或备用恢复码", "requires_2fa": true})
			return User{}, false
		}
		if !a.verifyCode(uid, in.Code) {
			fail(w, 401, "验证码无效、已使用或已过期")
			return User{}, false
		}
	}
	return u, true
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var in loginInput
	if !decode(w, r, &in) {
		return
	}
	u, ok := a.credentials(w, in)
	if ok {
		a.finishLogin(w, r, u, "console", true)
	}
}
func (a *App) finishLogin(w http.ResponseWriter, r *http.Request, u User, appID string, cookie bool) {
	raw := token()
	sid := id()
	ttl := int64(24 * 3600)
	if cookie {
		ttl = 7 * 24 * 3600
	}
	_, err := a.db.Exec("INSERT INTO sessions VALUES(?,?,?,?,?,?,?,?,?)", sid, u.ID, appID, hash(raw), ip(r), r.UserAgent(), now(), now(), now()+ttl)
	if err != nil {
		fail(w, 500, "无法创建会话")
		return
	}
	a.audit(u.ID, "auth.login", appID, ip(r))
	if cookie {
		a.setCookie(w, raw, int(ttl))
		jsonOut(w, 200, map[string]any{"user": u})
	} else {
		jsonOut(w, 200, map[string]any{"access_token": raw, "token_type": "Bearer", "expires_in": ttl, "user": u})
	}
}
func (a *App) me(w http.ResponseWriter, r *http.Request, s *session) { jsonOut(w, 200, s.User) }
func (a *App) logout(w http.ResponseWriter, r *http.Request, s *session) {
	_, _ = a.db.Exec("DELETE FROM sessions WHERE id=?", s.ID)
	a.setCookie(w, "", -1)
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) heartbeat(w http.ResponseWriter, r *http.Request, s *session) {
	_, _ = a.db.Exec("UPDATE sessions SET last_seen=? WHERE id=?", now(), s.ID)
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) sessions(w http.ResponseWriter, r *http.Request, s *session) {
	rows, e := a.db.Query("SELECT s.id,s.app_id,COALESCE(a.name,'Auth 控制台'),s.ip,s.agent,s.last_seen,s.created_at,s.expires_at FROM sessions s LEFT JOIN applications a ON s.app_id=a.id WHERE s.user_id=? AND s.expires_at>? ORDER BY s.last_seen DESC", s.UserID, now())
	if e != nil {
		fail(w, 500, "读取会话失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var sid, appID, name, addr, agent string
		var seen, created, exp int64
		if rows.Scan(&sid, &appID, &name, &addr, &agent, &seen, &created, &exp) != nil {
			continue
		}
		out = append(out, map[string]any{"id": sid, "app_id": appID, "app_name": name, "ip": addr, "agent": agent, "last_seen": seen, "created_at": created, "expires_at": exp, "online": now()-seen < 120, "current": sid == s.ID})
	}
	jsonOut(w, 200, out)
}
func (a *App) revokeSession(w http.ResponseWriter, r *http.Request, s *session) {
	res, err := a.db.Exec("DELETE FROM sessions WHERE id=? AND user_id=?", r.PathValue("id"), s.UserID)
	if err != nil {
		fail(w, 500, "撤销失败")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		fail(w, 404, "会话不存在")
		return
	}
	a.audit(s.UserID, "session.revoke", r.PathValue("id"), ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) verifyPassword(uid, p string) bool {
	var stored string
	_ = a.db.QueryRow("SELECT password FROM users WHERE id=?", uid).Scan(&stored)
	return bcrypt.CompareHashAndPassword([]byte(stored), []byte(p)) == nil
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		Password        string `json:"password"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !a.verifyPassword(s.UserID, in.CurrentPassword) {
		fail(w, 403, "当前密码不正确")
		return
	}
	if e := checkPassword(in.Password); e != nil {
		fail(w, 400, e.Error())
		return
	}
	if s.User.TFA && !a.verifyCode(s.UserID, in.Code) {
		fail(w, 403, "请输入有效的 2FA 验证码")
		return
	}
	p, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "操作失败")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE users SET password=? WHERE id=?", string(p), s.UserID)
	if e == nil {
		_, e = tx.Exec("DELETE FROM sessions WHERE user_id=? AND id<>?", s.UserID, s.ID)
	}
	if e == nil {
		_, e = tx.Exec("DELETE FROM recovery WHERE user_id=?", s.UserID)
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "修改失败")
		return
	}
	a.audit(s.UserID, "password.change", "", ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) verifyCode(uid, code string) bool {
	code = strings.TrimSpace(code)
	var enc string
	var last int64
	if a.db.QueryRow("SELECT totp,last_totp FROM users WHERE id=?", uid).Scan(&enc, &last) != nil || enc == "" {
		return false
	}
	secret, e := a.open(enc)
	if e != nil {
		return false
	}
	t := time.Now()
	for d := -1; d <= 1; d++ {
		at := t.Add(time.Duration(d) * 30 * time.Second)
		step := at.Unix() / 30
		if step <= last {
			continue
		}
		ok, _ := totp.ValidateCustom(code, secret, at, totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if ok {
			res, e := a.db.Exec("UPDATE users SET last_totp=? WHERE id=? AND last_totp<?", step, uid, step)
			if e != nil {
				return false
			}
			n, _ := res.RowsAffected()
			return n == 1
		}
	}
	res, e := a.db.Exec("DELETE FROM backup_codes WHERE user_id=? AND hash=?", uid, hash(code))
	if e != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}
func (a *App) totpSetup(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct{ Password string }
	if !decode(w, r, &in) {
		return
	}
	if s.User.TFA {
		fail(w, 409, "已启用两步验证")
		return
	}
	if !a.verifyPassword(s.UserID, in.Password) {
		fail(w, 403, "密码不正确")
		return
	}
	k, e := totp.Generate(totp.GenerateOpts{Issuer: "Auth Center", AccountName: s.User.Email})
	if e != nil {
		fail(w, 500, "生成密钥失败")
		return
	}
	_, e = a.db.Exec("UPDATE users SET pending_totp=? WHERE id=?", a.seal(k.Secret()), s.UserID)
	if e != nil {
		fail(w, 500, "保存密钥失败")
		return
	}
	jsonOut(w, 200, map[string]string{"secret": k.Secret(), "url": k.URL()})
}
func (a *App) totpEnable(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct{ Code string }
	if !decode(w, r, &in) {
		return
	}
	var enc string
	_ = a.db.QueryRow("SELECT pending_totp FROM users WHERE id=?", s.UserID).Scan(&enc)
	secret, e := a.open(enc)
	var matchedStep int64 = -1
	if e == nil && secret != "" {
		t := time.Now()
		for d := -1; d <= 1; d++ {
			at := t.Add(time.Duration(d) * 30 * time.Second)
			ok, _ := totp.ValidateCustom(in.Code, secret, at, totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
			if ok {
				matchedStep = at.Unix() / 30
				break
			}
		}
	}
	if matchedStep < 0 {
		fail(w, 400, "验证码不正确，请重新检查验证器")
		return
	}
	codes := []string{}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "操作失败")
		return
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE users SET totp=pending_totp,pending_totp='',last_totp=? WHERE id=? AND totp='' AND pending_totp=?", matchedStep, s.UserID, enc)
	if e != nil {
		fail(w, 500, "启用失败")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 409, "配置已变更，请重新开始")
		return
	}
	for i := 0; i < 8; i++ {
		c := token()[:12]
		codes = append(codes, c)
		if _, e = tx.Exec("INSERT INTO backup_codes VALUES(?,?)", s.UserID, hash(c)); e != nil {
			fail(w, 500, "生成备用码失败")
			return
		}
	}
	_, e = tx.Exec("DELETE FROM sessions WHERE user_id=? AND id<>?", s.UserID, s.ID)
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "启用失败")
		return
	}
	a.audit(s.UserID, "2fa.enable", "", ip(r))
	jsonOut(w, 200, map[string]any{"backup_codes": codes})
}
func (a *App) totpDisable(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct{ Password, Code string }
	if !decode(w, r, &in) {
		return
	}
	if !a.verifyPassword(s.UserID, in.Password) || !a.verifyCode(s.UserID, in.Code) {
		fail(w, 403, "密码或验证码不正确")
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "操作失败")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE users SET totp='',pending_totp='',last_totp=0 WHERE id=?", s.UserID)
	if e == nil {
		_, e = tx.Exec("DELETE FROM backup_codes WHERE user_id=?", s.UserID)
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "关闭失败")
		return
	}
	a.audit(s.UserID, "2fa.disable", "", ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) audits(w http.ResponseWriter, r *http.Request, s *session) {
	query := "SELECT a.action,a.detail,a.ip,a.created_at,COALESCE(u.email,'system') FROM audit a LEFT JOIN users u ON a.user_id=u.id"
	args := []any{}
	if s.User.Role != "admin" {
		query += " WHERE a.user_id=?"
		args = append(args, s.UserID)
	}
	query += " ORDER BY a.created_at DESC LIMIT 100"
	rows, e := a.db.Query(query, args...)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var action, detail, addr, email string
		var t int64
		_ = rows.Scan(&action, &detail, &addr, &t, &email)
		out = append(out, map[string]any{"action": action, "detail": detail, "ip": addr, "created_at": t, "email": email})
	}
	jsonOut(w, 200, out)
}
