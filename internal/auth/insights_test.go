package auth

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func profileInput(revision int) map[string]any {
	return map[string]any{"name": "测试用户", "phone": "+86 138-0000-0000", "organization": "示例团队", "department": "研发部", "job_title": "工程师", "location": "上海", "bio": "个人简介", "locale": "zh-CN", "time_zone": "Asia/Shanghai", "expected_revision": revision}
}

func TestProfileOwnershipValidationAndConflict(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	p := c.request("GET", "/api/profile", nil, 200)
	if p["revision"] != float64(0) || p["time_zone"] != "Asia/Shanghai" {
		t.Fatal(p)
	}
	input := profileInput(0)
	p = c.request("PUT", "/api/profile", input, 200)
	if p["revision"] != float64(1) || p["organization"] != "示例团队" {
		t.Fatal(p)
	}
	c.request("PUT", "/api/profile", input, 409)
	for _, field := range []string{"role", "email", "disabled", "user_id", "tags"} {
		invalid := profileInput(1)
		invalid[field] = "admin"
		c.request("PUT", "/api/profile", invalid, 400)
	}
	for _, tc := range []struct {
		field string
		value any
	}{{"name", " "}, {"name", strings.Repeat("中", 27)}, {"phone", "javascript:alert(1)"}, {"time_zone", "Local"}, {"time_zone", "Mars/Unknown"}, {"locale", "<script>"}, {"bio", strings.Repeat("x", 1001)}, {"organization", "bad\x00value"}, {"expected_revision", -1}} {
		in := profileInput(1)
		in[tc.field] = tc.value
		c.request("PUT", "/api/profile", in, 400)
	}
	missing := profileInput(1)
	delete(missing, "expected_revision")
	c.request("PUT", "/api/profile", missing, 400)
	u, err := c.a.user(uid)
	if err != nil || u.Name != "测试用户" || u.Role != "admin" {
		t.Fatal("profile altered role or failed to update name", u, err)
	}
	var details string
	if err = c.a.db.QueryRow("SELECT detail FROM audit WHERE action='profile.update'").Scan(&details); err != nil || details != uid {
		t.Fatal("profile audit must contain only subject ID", details, err)
	}
	adminCookie := c.cookie
	aid := c.app("Profile app")
	c.key = c.makeKey(aid, "auth:register", "auth:login", "session:read")
	registered := c.request("POST", "/v1/auth/register", map[string]string{"email": "other@example.com", "name": "Other", "password": "other-password"}, 201)
	otherID := registered["user"].(map[string]any)["id"].(string)
	login := c.request("POST", "/v1/auth/login", map[string]string{"email": "other@example.com", "password": "other-password"}, 200)
	c.bearer = login["access_token"].(string)
	c.cookie = nil
	c.request("GET", "/api/profile", nil, 401)
	c.request("PUT", "/api/profile", profileInput(0), 401)
	c.request("POST", "/api/auth/login", map[string]string{"email": "other@example.com", "password": "other-password"}, 200)
	c.request("PUT", "/api/profile", profileInput(0), 200)
	p = c.request("GET", "/api/profile", nil, 200)
	if p["revision"] != float64(1) {
		t.Fatal(p)
	}
	adminProfile, err := readProfile(c.a.db, uid)
	if err != nil || adminProfile.Revision != 1 {
		t.Fatal("other user profile mutated admin profile")
	}
	for _, role := range []string{"user", "developer"} {
		if _, err = c.a.db.Exec("UPDATE users SET role=? WHERE id=?", role, otherID); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/admin/analytics", "/api/admin/directory", "/api/admin/users/" + uid} {
			c.request("GET", path, nil, 403)
		}
		c.request("PUT", "/api/admin/users/"+uid+"/annotations", map[string]any{"tags": []string{"x"}, "expected_revision": 0}, 403)
	}
	c.cookie = adminCookie
	c.request("PUT", "/api/admin/users/"+otherID+"/annotations", map[string]any{"tags": []string{"试用用户"}, "notes": "内部备注", "expected_revision": 0}, 200)
	c.request("PUT", "/api/admin/users/"+otherID+"/annotations", map[string]any{"tags": []string{"覆盖"}, "expected_revision": 0}, 409)
	c.request("PUT", "/api/admin/users/"+otherID+"/annotations", map[string]any{"tags": []string{"重复", "重复"}, "expected_revision": 1}, 400)
	c.request("PUT", "/api/admin/users/missing/annotations", map[string]any{"tags": []string{}, "expected_revision": 0}, 404)
	detail := c.request("GET", "/api/admin/users/"+otherID, nil, 200)
	if detail["annotations"].(map[string]any)["notes"] != "内部备注" {
		t.Fatal(detail)
	}
	third := c.request("GET", "/v1/session", nil, 200)
	b, _ := json.Marshal(third)
	if strings.Contains(string(b), "内部备注") || strings.Contains(string(b), "138-0000") {
		t.Fatal("private profiles exposed through third-party session")
	}
	c.request("GET", "/api/admin/users/missing", nil, 404)
	c.cookie = nil
	c.bearer = ""
	c.request("GET", "/api/admin/analytics", nil, 401)
}

func TestActivityCountsSurviveLogoutAndDeduplicate(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	aid := c.app("Activity app")
	c.key = c.makeKey(aid, "auth:login", "session:read")
	login := c.request("POST", "/v1/auth/login", map[string]string{"email": "admin@example.com", "password": "admin-password-123"}, 200)
	c.bearer = login["access_token"].(string)
	for i := 0; i < 3; i++ {
		c.request("POST", "/v1/session/heartbeat", nil, 200)
	}
	c.request("POST", "/api/heartbeat", nil, 200)
	stats := c.request("GET", "/api/admin/analytics", nil, 200)
	summary := stats["summary"].(map[string]any)
	if summary["online_users"] != float64(1) || summary["active_today"] != float64(1) || summary["logins_30d"] != float64(2) {
		t.Fatal(summary)
	}
	days := stats["days"].([]any)
	if len(days) != 30 || days[29].(map[string]any)["logins"] != float64(2) {
		t.Fatal(days)
	}
	if len(stats["apps"].([]any)) != 2 {
		t.Fatal(stats)
	}
	c.request("POST", "/v1/auth/logout", nil, 200)
	c.request("POST", "/v1/session/heartbeat", nil, 401)
	day := (now() / 86400) * 86400
	for _, offset := range []int64{1, 6, 7, 29, 30} {
		at := day - offset*86400 + 10
		if _, err := c.a.db.Exec("INSERT INTO user_activity_daily VALUES(?,?,?,?,?,?)", uid, "history", day-offset*86400, 3, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.a.db.Exec("UPDATE sessions SET expires_at=?", now()-1); err != nil {
		t.Fatal(err)
	}
	// Use an independent valid observer session; no login activity is synthesized.
	observerRaw := token()
	if _, err := c.a.db.Exec("INSERT INTO sessions VALUES(?,?,?,?,?,?,?,?,?)", id(), uid, "console", hash(observerRaw), "192.0.2.1", "test", now()-300, now()-300, now()+1000); err != nil {
		t.Fatal(err)
	}
	c.cookie = &http.Cookie{Name: "auth_session", Value: observerRaw}
	stats = c.request("GET", "/api/admin/analytics", nil, 200)
	summary = stats["summary"].(map[string]any)
	if summary["online_users"] != float64(0) || summary["active_7d"] != float64(1) || summary["active_30d"] != float64(1) || summary["logins_30d"] != float64(14) {
		t.Fatal(summary)
	}
	if _, err := c.a.db.Exec("DELETE FROM sessions WHERE token_hash<>?", hash(observerRaw)); err != nil {
		t.Fatal(err)
	}
	detail := c.request("GET", "/api/admin/users/"+uid, nil, 200)
	if detail["analytics"].(map[string]any)["summary"].(map[string]any)["logins_30d"] != float64(14) {
		t.Fatal("logout erased history")
	}
	response, _ := json.Marshal(detail)
	for _, secret := range []string{"token_hash", "password", "pending_totp", "master.key"} {
		if bytes.Contains(response, []byte(secret)) {
			t.Fatal("secret field exposed", secret)
		}
	}
}

func TestDirectoryPaginationFiltersAndPermissions(t *testing.T) {
	c := newTest(t)
	c.setup()
	for i := 0; i < 27; i++ {
		if _, err := c.a.db.Exec("INSERT INTO users(id,email,name,password,role,created_at) VALUES(?,?,?,?,?,?)", id(), "member"+string(rune('A'+i))+"@example.test", "成员", dummyHash, "user", now()); err != nil {
			t.Fatal(err)
		}
	}
	first := c.request("GET", "/api/admin/directory", nil, 200)
	second := c.request("GET", "/api/admin/directory?page=2", nil, 200)
	if first["total"] != float64(28) || len(first["items"].([]any)) != 25 || len(second["items"].([]any)) != 3 {
		t.Fatal(first, second)
	}
	seen := map[string]bool{}
	for _, v := range append(first["items"].([]any), second["items"].([]any)...) {
		uid := v.(map[string]any)["user"].(map[string]any)["id"].(string)
		if seen[uid] {
			t.Fatal("duplicate page item")
		}
		seen[uid] = true
	}
	for _, tc := range []struct {
		query string
		total float64
	}{{"q=ADMIN", 1}, {"role=user", 27}, {"state=online", 1}, {"q=%25", 0}, {"q=%27%20OR%201%3D1", 0}} {
		result := c.request("GET", "/api/admin/directory?"+tc.query, nil, 200)
		if result["total"] != tc.total {
			t.Fatal(tc.query, result)
		}
	}
	for _, query := range []string{"page=0", "page=invalid", "page=1000001", "role=owner", "state=invalid"} {
		c.request("GET", "/api/admin/directory?"+query, nil, 400)
	}
}

func TestConcurrentProfileEditsAndHeartbeats(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	statuses := make(chan int, 2)
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			body, _ := json.Marshal(profileInput(0))
			r := httptest.NewRequest("PUT", "/api/profile", bytes.NewReader(body))
			r.AddCookie(c.cookie)
			w := httptest.NewRecorder()
			c.a.Handler().ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	group.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			r := httptest.NewRequest("POST", "/api/heartbeat", nil)
			r.AddCookie(c.cookie)
			w := httptest.NewRecorder()
			c.a.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Errorf("heartbeat=%d", w.Code)
			}
		}()
	}
	group.Wait()
	var count, logins int
	if err := c.a.db.QueryRow("SELECT COUNT(*),SUM(logins) FROM user_activity_daily WHERE user_id=?", uid).Scan(&count, &logins); err != nil || count != 1 || logins != 1 {
		t.Fatal(count, logins, err)
	}
}

func TestRealVersionOneMigrationAndRestart(t *testing.T) {
	path := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(path, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO users(id,email,name,password,role,totp,created_at) VALUES('legacy','legacy@example.test','旧用户',?,'admin','encrypted-value',?)", dummyHash, now()-86400); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO audit VALUES('a','legacy','auth.login','console','192.0.2.1',?)", now()-100); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO sessions VALUES('s','legacy','console','digest','192.0.2.1','test',?,?,?)", now(), now()-100, now()+100); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	key := bytes.Repeat([]byte{42}, 32)
	if err = os.WriteFile(filepath.Join(path, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		a, err := New(Config{DataDir: path})
		if err != nil {
			t.Fatal(err)
		}
		var secret, password string
		var count, logins, version int
		if err = a.db.QueryRow("SELECT password,totp FROM users WHERE id='legacy'").Scan(&password, &secret); err != nil || password != string(dummyHash) || secret != "encrypted-value" {
			t.Fatal("migration changed credentials", err)
		}
		if err = a.db.QueryRow("SELECT COUNT(*),SUM(logins) FROM user_activity_daily").Scan(&count, &logins); err != nil || count != 1 || logins != 1 {
			t.Fatal("migration duplicated historical counts", count, logins, err)
		}
		if err = a.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
			t.Fatal(version, err)
		}
		p, err := readProfile(a.db, "legacy")
		if err != nil || p.Name != "旧用户" || p.Revision != 0 {
			t.Fatal(p, err)
		}
		a.Close()
	}
	after, err := os.ReadFile(filepath.Join(path, "master.key"))
	if err != nil || !bytes.Equal(key, after) {
		t.Fatal("master key changed")
	}
}

func TestAnalyticsUTCWindowsAndDistinctUsers(t *testing.T) {
	c := newTest(t)
	day := (now() / 86400) * 86400
	at := day + 12*3600
	tx, err := c.a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, offset := range []int64{0, 6, 7, 29, 30, -1} {
		uid := fmt.Sprintf("window-%d", i)
		seen := day - offset*86400
		if _, err = tx.Exec("INSERT INTO users(id,email,name,password,role,created_at) VALUES(?,?,?,?,?,?)", uid, uid+"@example.test", uid, dummyHash, "user", seen); err != nil {
			t.Fatal(err)
		}
		if err = recordActivity(tx, uid, "app-a", seen, true); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err = recordActivity(tx, uid, "app-b", seen, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats, err := readAnalytics(tx, "", at)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.Today != 1 || stats.Summary.Week != 2 || stats.Summary.Month != 4 || stats.Summary.Logins != 5 {
		t.Fatal("UTC boundaries or user deduplication incorrect", stats.Summary)
	}
	if len(stats.Days) != 30 || stats.Days[0].Active != 1 || stats.Days[29].Active != 1 || stats.Days[29].Logins != 2 {
		t.Fatal("chart and summary window disagree", stats.Days)
	}
	old, err := readAnalytics(tx, "window-4", at)
	if err != nil || old.Summary.Month != 0 || old.Summary.LastSeen != day-30*86400 {
		t.Fatal("old activity must remain visible as last seen, outside 30-day counts", old.Summary, err)
	}
	future, err := readAnalytics(tx, "window-5", at)
	if err != nil || future.Summary.NewUsers != 0 || future.Summary.Month != 0 {
		t.Fatal("future records counted in the current window", future.Summary, err)
	}
}

func TestActivityAndProfileFailuresRollback(t *testing.T) {
	c := newTest(t)
	uid := c.setup()
	for _, source := range []string{"activity", "audit"} {
		t.Run("login-"+source, func(t *testing.T) {
			statement := "CREATE TRIGGER reject_write BEFORE INSERT ON user_activity_daily BEGIN SELECT RAISE(ABORT,'injected'); END"
			if source == "audit" {
				statement = "CREATE TRIGGER reject_write BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'injected'); END"
			}
			if _, err := c.a.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			body := bytes.NewBufferString(`{"email":"admin@example.com","password":"admin-password-123"}`)
			r := httptest.NewRequest("POST", "/api/auth/login", body)
			w := httptest.NewRecorder()
			c.a.Handler().ServeHTTP(w, r)
			if w.Code != 500 || len(w.Result().Cookies()) != 0 {
				t.Fatal("failed login returned credentials", w.Code)
			}
			var sessions, logins, audits int
			if err := c.a.db.QueryRow("SELECT (SELECT COUNT(*) FROM sessions),(SELECT SUM(logins) FROM user_activity_daily),(SELECT COUNT(*) FROM audit WHERE action='auth.login')").Scan(&sessions, &logins, &audits); err != nil || sessions != 1 || logins != 1 || audits != 1 {
				t.Fatal("partial login committed", sessions, logins, audits, err)
			}
			if _, err := c.a.db.Exec("DROP TRIGGER reject_write"); err != nil {
				t.Fatal(err)
			}
		})
	}
	before := now() - 300
	if _, err := c.a.db.Exec("UPDATE sessions SET last_seen=?", before); err != nil {
		t.Fatal(err)
	}
	if _, err := c.a.db.Exec("CREATE TRIGGER reject_activity BEFORE INSERT ON user_activity_daily BEGIN SELECT RAISE(ABORT,'injected'); END"); err != nil {
		t.Fatal(err)
	}
	c.request("POST", "/api/heartbeat", nil, 500)
	var seen int64
	if err := c.a.db.QueryRow("SELECT last_seen FROM sessions").Scan(&seen); err != nil || seen != before {
		t.Fatal("heartbeat committed without activity", seen, err)
	}
	if _, err := c.a.db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit BEGIN SELECT RAISE(ABORT,'injected'); END"); err != nil {
		t.Fatal(err)
	}
	c.request("PUT", "/api/profile", profileInput(0), 500)
	p, err := readProfile(c.a.db, uid)
	if err != nil || p.Revision != 0 || p.Name != "Admin" || p.Organization != "" {
		t.Fatal("profile committed without audit", p, err)
	}
	c.request("PUT", "/api/admin/users/"+uid+"/annotations", map[string]any{"tags": []string{"测试"}, "expected_revision": 0}, 500)
	notes, err := readAnnotations(c.a.db, uid)
	if err != nil || notes.Revision != 0 || len(notes.Tags) != 0 {
		t.Fatal("annotations committed without audit", notes, err)
	}
}
