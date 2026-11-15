package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func recordActivity(tx *sql.Tx, uid, appID string, at int64, login bool) error {
	count := 0
	if login {
		count = 1
	}
	_, err := tx.Exec(`INSERT INTO user_activity_daily(user_id,app_id,day,logins,first_seen,last_seen) VALUES(?,?,?,?,?,?)
 ON CONFLICT(user_id,app_id,day) DO UPDATE SET logins=logins+excluded.logins,last_seen=MAX(last_seen,excluded.last_seen)`, uid, appID, (at/86400)*86400, count, at, at)
	return err
}

type activityDay struct {
	Date     string `json:"date"`
	Active   int    `json:"active"`
	Logins   int    `json:"logins"`
	NewUsers int    `json:"new_users"`
}
type activitySummary struct {
	Total    int   `json:"total_users"`
	Enabled  int   `json:"enabled_users"`
	Disabled int   `json:"disabled_users"`
	TFA      int   `json:"tfa_users"`
	Online   int   `json:"online_users"`
	Today    int   `json:"active_today"`
	Week     int   `json:"active_7d"`
	Month    int   `json:"active_30d"`
	Logins   int   `json:"logins_30d"`
	NewUsers int   `json:"new_users_30d"`
	LastSeen int64 `json:"last_seen"`
}
type activityApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active int    `json:"active_users"`
	Logins int    `json:"logins"`
}
type activityGroup struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type analytics struct {
	GeneratedAt int64           `json:"generated_at"`
	Timezone    string          `json:"timezone"`
	Summary     activitySummary `json:"summary"`
	Days        []activityDay   `json:"days"`
	Apps        []activityApp   `json:"apps"`
	Roles       []activityGroup `json:"roles"`
}

// All figures in a response use one timestamp and a read transaction. Historical
// actives are distinct users, not session counts; deleted sessions do not erase them.
func readAnalytics(tx *sql.Tx, uid string, at int64) (analytics, error) {
	out := analytics{GeneratedAt: at, Timezone: "UTC", Days: []activityDay{}, Apps: []activityApp{}, Roles: []activityGroup{}}
	day := (at / 86400) * 86400
	start := day - 29*86400
	filter := ""
	args := []any{}
	if uid != "" {
		filter = " WHERE id=?"
		args = append(args, uid)
	}
	err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(disabled=0),0),COALESCE(SUM(disabled=1),0),COALESCE(SUM(totp<>''),0),COALESCE(SUM(created_at>=? AND created_at<=?),0) FROM users`+filter, append([]any{start, at}, args...)...).Scan(&out.Summary.Total, &out.Summary.Enabled, &out.Summary.Disabled, &out.Summary.TFA, &out.Summary.NewUsers)
	if err != nil {
		return out, err
	}
	// Bound the aggregate to the chart window so accumulated history does not
	// require a full scan and future-dated rows cannot inflate the counters.
	activityFilter := " WHERE day>=? AND day<=?"
	activityArgs := []any{day, day - 6*86400, start, day}
	if uid != "" {
		activityFilter += " AND user_id=?"
		activityArgs = append(activityArgs, uid)
	}
	err = tx.QueryRow(`SELECT COUNT(DISTINCT CASE WHEN day>=? THEN user_id END),COUNT(DISTINCT CASE WHEN day>=? THEN user_id END),COUNT(DISTINCT user_id),COALESCE(SUM(logins),0) FROM user_activity_daily`+activityFilter, activityArgs...).Scan(&out.Summary.Today, &out.Summary.Week, &out.Summary.Month, &out.Summary.Logins)
	if err != nil {
		return out, err
	}
	lastFilter := ""
	var lastArgs []any
	if uid != "" {
		lastFilter = " WHERE user_id=?"
		lastArgs = []any{uid}
	}
	if err = tx.QueryRow("SELECT COALESCE(MAX(last_seen),0) FROM user_activity_daily"+lastFilter, lastArgs...).Scan(&out.Summary.LastSeen); err != nil {
		return out, err
	}
	q := `SELECT COUNT(DISTINCT s.user_id) FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.expires_at>? AND s.last_seen>? AND u.disabled=0`
	args = []any{at, at - 120}
	if uid != "" {
		q += " AND s.user_id=?"
		args = append(args, uid)
	}
	if err = tx.QueryRow(q, args...).Scan(&out.Summary.Online); err != nil {
		return out, err
	}
	q = `SELECT day,COUNT(DISTINCT user_id),SUM(logins) FROM user_activity_daily WHERE day>=? AND day<=?`
	args = []any{start, day}
	if uid != "" {
		q += " AND user_id=?"
		args = append(args, uid)
	}
	q += " GROUP BY day ORDER BY day"
	rows, err := tx.Query(q, args...)
	if err != nil {
		return out, err
	}
	byDay := map[int64]activityDay{}
	for rows.Next() {
		var d int64
		var item activityDay
		if err = rows.Scan(&d, &item.Active, &item.Logins); err != nil {
			break
		}
		byDay[d] = item
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	q = "SELECT (created_at/86400)*86400,COUNT(*) FROM users WHERE created_at>=? AND created_at<=?"
	args = []any{start, at}
	if uid != "" {
		q += " AND id=?"
		args = append(args, uid)
	}
	q += " GROUP BY (created_at/86400)*86400"
	rows, err = tx.Query(q, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d int64
		var count int
		if err = rows.Scan(&d, &count); err != nil {
			break
		}
		item := byDay[d]
		item.NewUsers = count
		byDay[d] = item
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	for d := start; d <= day; d += 86400 {
		item := byDay[d]
		item.Date = time.Unix(d, 0).UTC().Format("2006-01-02")
		out.Days = append(out.Days, item)
	}
	q = `SELECT d.app_id,CASE WHEN d.app_id='console' THEN 'Auth 控制台' ELSE COALESCE(a.name,'历史应用') END,COUNT(DISTINCT d.user_id),SUM(d.logins) FROM user_activity_daily d LEFT JOIN applications a ON a.id=d.app_id WHERE d.day>=? AND d.day<=?`
	args = []any{start, day}
	if uid != "" {
		q += " AND d.user_id=?"
		args = append(args, uid)
	}
	q += " GROUP BY d.app_id ORDER BY COUNT(DISTINCT d.user_id) DESC,SUM(d.logins) DESC,d.app_id LIMIT 20"
	rows, err = tx.Query(q, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item activityApp
		if err = rows.Scan(&item.ID, &item.Name, &item.Active, &item.Logins); err != nil {
			break
		}
		out.Apps = append(out.Apps, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	q = "SELECT role,COUNT(*) FROM users"
	args = nil
	if uid != "" {
		q += " WHERE id=?"
		args = append(args, uid)
	}
	q += " GROUP BY role ORDER BY role"
	rows, err = tx.Query(q, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item activityGroup
		if err = rows.Scan(&item.Name, &item.Count); err != nil {
			break
		}
		out.Roles = append(out.Roles, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return out, err
}

func (a *App) adminAnalytics(w http.ResponseWriter, r *http.Request, s *session) {
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		fail(w, 500, "读取分析失败")
		return
	}
	defer tx.Rollback()
	out, err := readAnalytics(tx, "", now())
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "读取分析失败")
		return
	}
	jsonOut(w, 200, out)
}

func (a *App) userDirectory(w http.ResponseWriter, r *http.Request, s *session) {
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 1000000 {
			fail(w, 400, "页码无效")
			return
		}
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 160 {
		fail(w, 400, "搜索内容过长")
		return
	}
	role := r.URL.Query().Get("role")
	if role != "" && role != "admin" && role != "developer" && role != "user" {
		fail(w, 400, "角色无效")
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "enabled" && state != "disabled" && state != "online" {
		fail(w, 400, "状态无效")
		return
	}
	at := now()
	where := " WHERE 1=1"
	args := []any{}
	if query != "" {
		where += ` AND (instr(lower(u.name),lower(?))>0 OR instr(lower(u.email),lower(?))>0 OR instr(lower(COALESCE(p.organization,'')),lower(?))>0 OR instr(lower(COALESCE(p.department,'')),lower(?))>0 OR instr(lower(COALESCE(n.tags,'')),lower(?))>0)`
		for i := 0; i < 5; i++ {
			args = append(args, query)
		}
	}
	if role != "" {
		where += " AND u.role=?"
		args = append(args, role)
	}
	if state == "enabled" {
		where += " AND u.disabled=0"
	}
	if state == "disabled" {
		where += " AND u.disabled=1"
	}
	if state == "online" {
		where += " AND u.disabled=0 AND EXISTS(SELECT 1 FROM sessions s WHERE s.user_id=u.id AND s.expires_at>? AND s.last_seen>?)"
		args = append(args, at, at-120)
	}
	from := ` FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id LEFT JOIN user_annotations n ON n.user_id=u.id`
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	defer tx.Rollback()
	var total int
	if err = tx.QueryRow("SELECT COUNT(*)"+from+where, args...).Scan(&total); err != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	q := `SELECT u.id,u.email,u.name,u.role,u.disabled,u.totp<>'',u.created_at,COALESCE(p.organization,''),COALESCE(p.department,''),COALESCE(n.tags,'[]'),COALESCE((SELECT MAX(last_seen) FROM user_activity_daily d WHERE d.user_id=u.id),0),(SELECT COUNT(*) FROM sessions s WHERE s.user_id=u.id AND s.expires_at>` + strconv.FormatInt(at, 10) + ` AND s.last_seen>` + strconv.FormatInt(at-120, 10) + ` AND u.disabled=0)` + from + where + ` ORDER BY u.created_at DESC,u.id LIMIT 25 OFFSET ?`
	rows, err := tx.Query(q, append(args, (page-1)*25)...)
	if err != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	items := []map[string]any{}
	for rows.Next() {
		var u User
		var org, dept, tags string
		var seen int64
		var online int
		if err = rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Disabled, &u.TFA, &u.Created, &org, &dept, &tags, &seen, &online); err != nil {
			break
		}
		var list []string
		if err = json.Unmarshal([]byte(tags), &list); err != nil {
			break
		}
		items = append(items, map[string]any{"user": u, "organization": org, "department": dept, "tags": list, "last_seen": seen, "online": online > 0})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	jsonOut(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": 25, "generated_at": at})
}

func (a *App) adminUserDetail(w http.ResponseWriter, r *http.Request, s *session) {
	tx, err := a.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	defer tx.Rollback()
	uid := r.PathValue("id")
	u, err := scanUser(tx.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", uid))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "用户不存在")
		return
	}
	if err != nil {
		fail(w, 500, "读取用户失败")
		return
	}
	p, err := readProfile(tx, uid)
	if err != nil {
		fail(w, 500, "读取资料失败")
		return
	}
	notes, err := readAnnotations(tx, uid)
	if err != nil {
		fail(w, 500, "读取标签失败")
		return
	}
	stats, err := readAnalytics(tx, uid, now())
	if err != nil {
		fail(w, 500, "读取活跃情况失败")
		return
	}
	rows, err := tx.Query("SELECT action,detail,ip,created_at FROM audit WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 50", uid)
	if err != nil {
		fail(w, 500, "读取活动失败")
		return
	}
	events := []map[string]any{}
	for rows.Next() {
		var action, detail, addr string
		var at int64
		if err = rows.Scan(&action, &detail, &addr, &at); err != nil {
			break
		}
		events = append(events, map[string]any{"action": action, "detail": detail, "ip": addr, "created_at": at, "email": u.Email})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, 500, "读取活动失败")
		return
	}
	rows, err = tx.Query(`SELECT s.id,s.app_id,COALESCE(a.name,'Auth 控制台'),s.ip,s.agent,s.last_seen,s.created_at,s.expires_at FROM sessions s LEFT JOIN applications a ON a.id=s.app_id WHERE s.user_id=? AND s.expires_at>? ORDER BY s.last_seen DESC LIMIT 50`, uid, stats.GeneratedAt)
	if err != nil {
		fail(w, 500, "读取会话失败")
		return
	}
	sessions := []map[string]any{}
	for rows.Next() {
		var sid, aid, name, addr, agent string
		var seen, created, expires int64
		if err = rows.Scan(&sid, &aid, &name, &addr, &agent, &seen, &created, &expires); err != nil {
			break
		}
		sessions = append(sessions, map[string]any{"id": sid, "app_id": aid, "app_name": name, "ip": addr, "agent": agent, "last_seen": seen, "created_at": created, "expires_at": expires, "online": !u.Disabled && seen > stats.GeneratedAt-120})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "读取会话失败")
		return
	}
	jsonOut(w, 200, map[string]any{"user": u, "profile": p, "annotations": notes, "analytics": stats, "audits": events, "sessions": sessions})
}
