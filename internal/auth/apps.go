package auth

import (
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"strings"
)

func (a *App) ownsApp(uid, role, appID string) bool {
	var owner string
	e := a.db.QueryRow("SELECT owner_id FROM applications WHERE id=?", appID).Scan(&owner)
	return e == nil && (role == "admin" || uid == owner)
}
func (a *App) applications(w http.ResponseWriter, r *http.Request, s *session) {
	q := "SELECT a.id,a.name,a.description,a.owner_id,a.created_at,(SELECT COUNT(*) FROM api_keys k WHERE k.app_id=a.id AND k.revoked=0) FROM applications a"
	args := []any{}
	if s.User.Role != "admin" {
		q += " WHERE a.owner_id=?"
		args = append(args, s.UserID)
	}
	q += " ORDER BY a.created_at DESC"
	rows, e := a.db.Query(q, args...)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, desc, owner string
		var t int64
		var count int
		_ = rows.Scan(&id, &name, &desc, &owner, &t, &count)
		out = append(out, map[string]any{"id": id, "name": name, "description": desc, "owner_id": owner, "created_at": t, "key_count": count})
	}
	jsonOut(w, 200, out)
}
func (a *App) createApplication(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct{ Name, Description string }
	if !decode(w, r, &in) {
		return
	}
	if !validText(in.Name, 80) || len(in.Description) > 500 {
		fail(w, 400, "应用名称必填，描述最多 500 字节")
		return
	}
	aid := id()
	_, e := a.db.Exec("INSERT INTO applications VALUES(?,?,?,?,?)", aid, in.Name, in.Description, s.UserID, now())
	if e != nil {
		fail(w, 500, "创建失败")
		return
	}
	a.audit(s.UserID, "application.create", in.Name, ip(r))
	jsonOut(w, 201, map[string]string{"id": aid})
}
func (a *App) keys(w http.ResponseWriter, r *http.Request, s *session) {
	q := "SELECT k.id,k.app_id,a.name,k.name,k.prefix,k.scopes,k.created_at,k.last_used,k.revoked FROM api_keys k JOIN applications a ON k.app_id=a.id"
	args := []any{}
	if s.User.Role != "admin" {
		q += " WHERE a.owner_id=?"
		args = append(args, s.UserID)
	}
	q += " ORDER BY k.created_at DESC"
	rows, e := a.db.Query(q, args...)
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, aid, app, name, prefix, scopes string
		var created, used int64
		var revoked bool
		_ = rows.Scan(&id, &aid, &app, &name, &prefix, &scopes, &created, &used, &revoked)
		out = append(out, map[string]any{"id": id, "app_id": aid, "app_name": app, "name": name, "prefix": prefix, "scopes": strings.Split(scopes, ","), "created_at": created, "last_used": used, "revoked": revoked})
	}
	jsonOut(w, 200, out)
}
func (a *App) createKey(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		AppID  string   `json:"app_id"`
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !a.ownsApp(s.UserID, s.User.Role, in.AppID) {
		fail(w, 403, "没有此应用的管理权限")
		return
	}
	if !validText(in.Name, 80) || len(in.Scopes) == 0 {
		fail(w, 400, "请填写密钥名称并选择权限")
		return
	}
	seen := map[string]bool{}
	for _, v := range in.Scopes {
		if (v != "auth:register" && v != "auth:login" && v != "session:read") || seen[v] {
			fail(w, 400, "权限范围无效或重复")
			return
		}
		seen[v] = true
	}
	raw := "ak_" + token()
	kid := id()
	_, e := a.db.Exec("INSERT INTO api_keys(id,app_id,name,prefix,hash,scopes,created_at) VALUES(?,?,?,?,?,?,?)", kid, in.AppID, in.Name, raw[:11], hash(raw), strings.Join(in.Scopes, ","), now())
	if e != nil {
		fail(w, 500, "创建失败")
		return
	}
	a.audit(s.UserID, "key.create", in.AppID+" "+in.Name, ip(r))
	jsonOut(w, 201, map[string]string{"id": kid, "key": raw})
}
func (a *App) revokeKey(w http.ResponseWriter, r *http.Request, s *session) {
	var aid string
	if a.db.QueryRow("SELECT app_id FROM api_keys WHERE id=?", r.PathValue("id")).Scan(&aid) != nil {
		fail(w, 404, "密钥不存在")
		return
	}
	if !a.ownsApp(s.UserID, s.User.Role, aid) {
		fail(w, 403, "没有此应用的管理权限")
		return
	}
	_, e := a.db.Exec("UPDATE api_keys SET revoked=1 WHERE id=?", r.PathValue("id"))
	if e != nil {
		fail(w, 500, "撤销失败")
		return
	}
	a.audit(s.UserID, "key.revoke", r.PathValue("id"), ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) apiKey(w http.ResponseWriter, r *http.Request, scope string) (string, bool) {
	var kid, aid, scopes, role string
	var disabled bool
	e := a.db.QueryRow("SELECT k.id,k.app_id,k.scopes,u.role,u.disabled FROM api_keys k JOIN applications a ON k.app_id=a.id JOIN users u ON a.owner_id=u.id WHERE k.hash=? AND k.revoked=0", hash(r.Header.Get("X-API-Key"))).Scan(&kid, &aid, &scopes, &role, &disabled)
	if e != nil || disabled || (role != "admin" && role != "developer") {
		fail(w, 401, "API Key 无效或已撤销")
		return "", false
	}
	found := scope == ""
	for _, v := range strings.Split(scopes, ",") {
		if v == scope {
			found = true
		}
	}
	if !found {
		fail(w, 403, "API Key 缺少权限："+scope)
		return "", false
	}
	_, _ = a.db.Exec("UPDATE api_keys SET last_used=? WHERE id=?", now(), kid)
	return aid, true
}
func (a *App) thirdRegister(w http.ResponseWriter, r *http.Request) {
	aid, ok := a.apiKey(w, r, "auth:register")
	if !ok {
		return
	}
	var in struct{ Email, Name, Password string }
	if !decode(w, r, &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !emailValid(in.Email) || !validText(in.Name, 80) {
		fail(w, 400, "邮箱或姓名不正确")
		return
	}
	if e := checkPassword(in.Password); e != nil {
		fail(w, 400, e.Error())
		return
	}
	var count int
	_ = a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count == 0 {
		fail(w, 409, "请先初始化系统")
		return
	}
	p, _ := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	uid := id()
	_, e := a.db.Exec("INSERT INTO users(id,email,name,password,role,created_at) VALUES(?,?,?,?,'user',?)", uid, in.Email, in.Name, string(p), now())
	if e != nil {
		fail(w, 409, "此邮箱无法注册")
		return
	}
	a.audit(uid, "auth.register", aid, ip(r))
	u, _ := a.user(uid)
	jsonOut(w, 201, map[string]any{"user": u})
}
func (a *App) thirdLogin(w http.ResponseWriter, r *http.Request) {
	aid, ok := a.apiKey(w, r, "auth:login")
	if !ok {
		return
	}
	var in loginInput
	if !decode(w, r, &in) {
		return
	}
	u, ok := a.credentials(w, in)
	if ok {
		a.finishLogin(w, r, u, aid, false)
	}
}
func (a *App) thirdAuth(w http.ResponseWriter, r *http.Request, scope string) (*session, bool) {
	aid, ok := a.apiKey(w, r, scope)
	if !ok {
		return nil, false
	}
	s, e := a.session(r, false)
	if e != nil || s.AppID != aid {
		fail(w, 401, "会话无效或不属于此应用")
		return nil, false
	}
	return s, true
}
func (a *App) thirdHeartbeat(w http.ResponseWriter, r *http.Request) {
	s, ok := a.thirdAuth(w, r, "session:read")
	if ok {
		a.heartbeat(w, r, s)
	}
}
func (a *App) thirdSession(w http.ResponseWriter, r *http.Request) {
	s, ok := a.thirdAuth(w, r, "session:read")
	if ok {
		jsonOut(w, 200, map[string]any{"user": s.User, "app_id": s.AppID, "session_id": s.ID})
	}
}
func (a *App) thirdLogout(w http.ResponseWriter, r *http.Request) {
	s, ok := a.thirdAuth(w, r, "")
	if ok {
		_, _ = a.db.Exec("DELETE FROM sessions WHERE id=?", s.ID)
		jsonOut(w, 200, map[string]bool{"ok": true})
	}
}
func (a *App) users(w http.ResponseWriter, r *http.Request, s *session) {
	rows, e := a.db.Query("SELECT " + userColumns + " FROM users ORDER BY created_at DESC")
	if e != nil {
		fail(w, 500, "读取失败")
		return
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, e := scanUser(rows)
		if e == nil {
			out = append(out, u)
		}
	}
	jsonOut(w, 200, out)
}
func (a *App) updateUser(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Role     string `json:"role"`
		Disabled bool   `json:"disabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role != "admin" && in.Role != "developer" && in.Role != "user" {
		fail(w, 400, "角色无效")
		return
	}
	uid := r.PathValue("id")
	if uid == s.UserID && (in.Role != "admin" || in.Disabled) {
		fail(w, 400, "不能降级或停用自己的管理员账户")
		return
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 500, "更新失败")
		return
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE users SET role=?,disabled=? WHERE id=?", in.Role, in.Disabled, uid)
	if e != nil {
		fail(w, 500, "更新失败")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		fail(w, 404, "用户不存在")
		return
	}
	var admins int
	if err := tx.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0").Scan(&admins); err != nil || admins == 0 {
		fail(w, 400, "系统必须保留至少一位有效管理员")
		return
	}
	if in.Disabled {
		_, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", uid)
	}
	if e != nil || tx.Commit() != nil {
		fail(w, 500, "更新失败")
		return
	}
	a.audit(s.UserID, "user.update", uid+" "+in.Role, ip(r))
	jsonOut(w, 200, map[string]bool{"ok": true})
}
