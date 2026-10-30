package auth

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type MailConfig struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password,omitempty"`
	From        string `json:"from"`
	TLS         string `json:"tls"`
	HasPassword bool   `json:"has_password"`
}

func (a *App) mailConfig() (MailConfig, error) {
	var enc string
	e := a.db.QueryRow("SELECT value FROM settings WHERE key='mail'").Scan(&enc)
	if e == sql.ErrNoRows {
		return MailConfig{Port: 587, TLS: "starttls"}, nil
	}
	if e != nil {
		return MailConfig{}, e
	}
	v, e := a.open(enc)
	if e != nil {
		return MailConfig{}, e
	}
	var m MailConfig
	e = json.Unmarshal([]byte(v), &m)
	return m, e
}
func (a *App) getMail(w http.ResponseWriter, r *http.Request, s *session) {
	m, e := a.mailConfig()
	if e != nil {
		fail(w, 500, "读取邮件配置失败")
		return
	}
	m.HasPassword = m.Password != ""
	m.Password = ""
	jsonOut(w, 200, m)
}
func (a *App) putMail(w http.ResponseWriter, r *http.Request, s *session) {
	var m MailConfig
	if !decode(w, r, &m) {
		return
	}
	old, e := a.mailConfig()
	if e != nil {
		fail(w, 500, "读取邮件配置失败")
		return
	}
	if m.Password == "" {
		m.Password = old.Password
	}
	if m.Enabled && (!validText(m.Host, 253) || strings.ContainsAny(m.Host, "\r\n /:") || m.Port < 1 || m.Port > 65535 || !emailValid(m.From) || (m.TLS != "tls" && m.TLS != "starttls") || strings.ContainsAny(m.Username, "\r\n")) {
		fail(w, 400, "请填写有效的 SMTP 主机、端口、发件邮箱和 TLS 模式")
		return
	}
	m.HasPassword = false
	b, _ := json.Marshal(m)
	if _, e = a.db.Exec("INSERT INTO settings(key,value) VALUES('mail',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", a.seal(string(b))); e != nil {
		fail(w, 500, "保存失败")
		return
	}
	a.audit(s.UserID, "mail.update", m.Host, ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func sendMail(m MailConfig, to, subject, body string) error {
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	var conn net.Conn
	var e error
	cfg := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	if m.TLS == "tls" {
		conn, e = tls.DialWithDialer(dialer, "tcp", addr, cfg)
	} else {
		conn, e = dialer.Dial("tcp", addr)
	}
	if e != nil {
		return e
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	c, e := smtp.NewClient(conn, m.Host)
	if e != nil {
		return e
	}
	defer c.Close()
	if m.TLS != "tls" {
		if e = c.StartTLS(cfg); e != nil {
			return e
		}
	}
	if m.Username != "" {
		if e = c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); e != nil {
			return e
		}
	}
	if e = c.Mail(m.From); e != nil {
		return e
	}
	if e = c.Rcpt(to); e != nil {
		return e
	}
	writer, e := c.Data()
	if e != nil {
		return e
	}
	_, e = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", m.From, to, subject, body)
	if e != nil {
		return e
	}
	if e = writer.Close(); e != nil {
		return e
	}
	return c.Quit()
}
func (a *App) testMail(w http.ResponseWriter, r *http.Request, s *session) {
	m, e := a.mailConfig()
	if e != nil || !m.Enabled {
		fail(w, 400, "请先保存并启用邮件配置")
		return
	}
	if e = sendMail(m, s.User.Email, "Auth Center SMTP test", "邮件配置测试成功。"); e != nil {
		fail(w, 502, "邮件发送失败，请检查 SMTP 地址、TLS 和账号授权码")
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) newRecovery(uid string) (string, error) {
	raw := token()
	tx, e := a.db.Begin()
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM recovery WHERE user_id=?", uid); e != nil {
		return "", e
	}
	if _, e = tx.Exec("INSERT INTO recovery VALUES(?,?,?,?,0)", id(), uid, hash(raw), now()+1800); e != nil {
		return "", e
	}
	return raw, tx.Commit()
}
func (a *App) recoveryURL(raw string) string {
	return strings.TrimRight(a.cfg.PublicURL, "/") + "/?reset=" + url.QueryEscape(raw)
}
func (a *App) forgot(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email string }
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.TrimSpace(in.Email)
	m, e := a.mailConfig()
	if e != nil || !m.Enabled {
		fail(w, 409, "未配置邮件，请向管理员提交找回申诉")
		return
	}
	var uid string
	e = a.db.QueryRow("SELECT id FROM users WHERE email=? AND disabled=0", strings.TrimSpace(in.Email)).Scan(&uid)
	if e == nil {
		raw, err := a.newRecovery(uid)
		if err == nil {
			if err = sendMail(m, in.Email, "Auth Center password recovery", "请在 30 分钟内打开链接重设密码：\n"+a.recoveryURL(raw)+"\n如果不是你本人操作，请忽略此邮件。"); err != nil {
				a.audit(uid, "recovery.mail_failed", "SMTP delivery failed", ip(r))
			}
		}
	}
	jsonOut(w, 200, map[string]string{"message": "如果账户存在，将收到密码重置邮件；请检查垃圾邮件"})
}
func (a *App) reset(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Password string }
	if !decode(w, r, &in) {
		return
	}
	if e := checkPassword(in.Password); e != nil {
		fail(w, 400, e.Error())
		return
	}
	var uid string
	if a.db.QueryRow("SELECT r.user_id FROM recovery r JOIN users u ON u.id=r.user_id WHERE r.hash=? AND r.used=0 AND r.expires_at>? AND u.disabled=0", hash(in.Token), now()).Scan(&uid) != nil {
		fail(w, 400, "找回链接无效或已过期")
		return
	}
	p, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "重置失败")
		return
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE recovery SET used=1 WHERE hash=? AND used=0 AND expires_at>?", hash(in.Token), now())
	if e != nil {
		fail(w, 500, "重置失败")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 400, "找回链接无效或已过期")
		return
	}
	_, e = tx.Exec("UPDATE users SET password=? WHERE id=?", string(p), uid)
	if e == nil {
		_, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", uid)
	}
	if e == nil {
		_, e = tx.Exec("DELETE FROM recovery WHERE user_id=?", uid)
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "重置失败")
		return
	}
	a.audit(uid, "password.recover", "", ip(r))
	jsonOut(w, 200, map[string]string{"message": "密码已重置，请重新登录。已启用的两步验证仍然有效。"})
}
func (a *App) appeal(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Contact, Reason string }
	if !decode(w, r, &in) {
		return
	}
	if !emailValid(in.Email) || !validText(in.Contact, 300) || !validText(in.Reason, 2000) {
		fail(w, 400, "请填写账户邮箱、可联系的方式和申诉说明")
		return
	}
	_, e := a.db.Exec("INSERT OR IGNORE INTO appeals(id,email,contact,reason,created_at) VALUES(?,?,?,?,?)", id(), strings.ToLower(in.Email), in.Contact, in.Reason, now())
	if e != nil {
		fail(w, 500, "提交失败，请稍后重试")
		return
	}
	jsonOut(w, 200, map[string]string{"message": "申诉已提交，请等待管理员核实身份后联系你"})
}
func (a *App) appeals(w http.ResponseWriter, r *http.Request, s *session) {
	rows, e := a.db.Query("SELECT id,email,contact,reason,status,note,created_at,resolved_at FROM appeals ORDER BY created_at DESC LIMIT 200")
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, email, contact, reason, status, note string
		var created, resolved int64
		_ = rows.Scan(&id, &email, &contact, &reason, &status, &note, &created, &resolved)
		out = append(out, map[string]any{"id": id, "email": email, "contact": contact, "reason": reason, "status": status, "note": note, "created_at": created, "resolved_at": resolved})
	}
	jsonOut(w, 200, out)
}
func (a *App) resolveAppeal(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Approve  bool   `json:"approve"`
		Note     string `json:"note"`
		Reset2FA bool   `json:"reset_2fa"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validText(in.Note, 2000) {
		fail(w, 400, "请填写核实结果与处理说明")
		return
	}
	var email string
	if a.db.QueryRow("SELECT email FROM appeals WHERE id=? AND status='pending'", r.PathValue("id")).Scan(&email) != nil {
		fail(w, 404, "待处理申诉不存在")
		return
	}
	var uid, raw string
	status := "rejected"
	if in.Approve {
		if a.db.QueryRow("SELECT id FROM users WHERE email=? AND disabled=0", email).Scan(&uid) != nil {
			fail(w, 404, "账户不存在或已停用，可拒绝此申诉")
			return
		}
		if uid == s.UserID && in.Reset2FA {
			fail(w, 400, "请由其他管理员处理自己的验证器恢复申诉")
			return
		}
		status = "approved"
		raw = token()
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "处理失败")
		return
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE appeals SET status=?,note=?,resolved_at=? WHERE id=? AND status='pending'", status, in.Note, now(), r.PathValue("id"))
	if e != nil {
		fail(w, 500, "处理失败")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 409, "申诉已被处理")
		return
	}
	if in.Approve {
		_, e = tx.Exec("DELETE FROM recovery WHERE user_id=?", uid)
		if e == nil {
			_, e = tx.Exec("INSERT INTO recovery VALUES(?,?,?,?,0)", id(), uid, hash(raw), now()+1800)
		}
		if e == nil && in.Reset2FA {
			_, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", uid)
		}
		if e == nil && in.Reset2FA {
			_, e = tx.Exec("UPDATE users SET totp='',pending_totp='',last_totp=0 WHERE id=?", uid)
			if e == nil {
				_, e = tx.Exec("DELETE FROM backup_codes WHERE user_id=?", uid)
			}
		}
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "处理失败")
		return
	}
	a.audit(s.UserID, "appeal."+status, email+fmt.Sprintf(" reset_2fa=%t", in.Reset2FA), ip(r))
	out := map[string]any{"ok": true}
	if raw != "" {
		out["recovery_url"] = a.recoveryURL(raw)
	}
	jsonOut(w, 200, out)
}
