package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
)

type Profile struct {
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	Organization string `json:"organization"`
	Department   string `json:"department"`
	JobTitle     string `json:"job_title"`
	Location     string `json:"location"`
	Bio          string `json:"bio"`
	Locale       string `json:"locale"`
	TimeZone     string `json:"time_zone"`
	Revision     int64  `json:"revision"`
	UpdatedAt    int64  `json:"updated_at"`
}

type queryRower interface {
	QueryRow(string, ...any) *sql.Row
}

func readProfile(db queryRower, uid string) (Profile, error) {
	var p Profile
	err := db.QueryRow(`SELECT u.name,COALESCE(p.phone,''),COALESCE(p.organization,''),COALESCE(p.department,''),COALESCE(p.job_title,''),COALESCE(p.location,''),COALESCE(p.bio,''),COALESCE(p.locale,'zh-CN'),COALESCE(p.time_zone,'Asia/Shanghai'),COALESCE(p.revision,0),COALESCE(p.updated_at,0)
 FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id WHERE u.id=?`, uid).Scan(&p.Name, &p.Phone, &p.Organization, &p.Department, &p.JobTitle, &p.Location, &p.Bio, &p.Locale, &p.TimeZone, &p.Revision, &p.UpdatedAt)
	return p, err
}

func (a *App) profile(w http.ResponseWriter, r *http.Request, s *session) {
	p, err := readProfile(a.db, s.UserID)
	if err != nil {
		fail(w, 500, "读取个人资料失败")
		return
	}
	jsonOut(w, 200, p)
}

var phonePattern = regexp.MustCompile(`^[+0-9() .-]{0,32}$`)
var localePattern = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z][a-zA-Z]{1,7})?(-[A-Z]{2})?$`)

func (p *Profile) validate() bool {
	for _, f := range []struct {
		value *string
		limit int
	}{
		{&p.Name, 80}, {&p.Phone, 32}, {&p.Organization, 160}, {&p.Department, 120}, {&p.JobTitle, 120}, {&p.Location, 160}, {&p.Bio, 1000}, {&p.Locale, 35}, {&p.TimeZone, 80},
	} {
		*f.value = strings.TrimSpace(*f.value)
		if len(*f.value) > f.limit || strings.IndexFunc(*f.value, func(c rune) bool { return unicode.IsControl(c) && c != '\n' && c != '\t' }) >= 0 {
			return false
		}
	}
	_, err := time.LoadLocation(p.TimeZone)
	return validText(p.Name, 80) && phonePattern.MatchString(p.Phone) && localePattern.MatchString(p.Locale) && p.TimeZone != "Local" && err == nil
}

func (a *App) updateProfile(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Profile
		Expected *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Expected == nil || *in.Expected < 0 || !in.Profile.validate() {
		fail(w, 400, "资料格式无效：姓名必填，请检查字段长度、电话、语言和 IANA 时区")
		return
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO user_profiles(user_id) VALUES(?) ON CONFLICT(user_id) DO NOTHING", s.UserID)
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	p := in.Profile
	res, err := tx.Exec(`UPDATE user_profiles SET phone=?,organization=?,department=?,job_title=?,location=?,bio=?,locale=?,time_zone=?,revision=revision+1,updated_at=? WHERE user_id=? AND revision=?`, p.Phone, p.Organization, p.Department, p.JobTitle, p.Location, p.Bio, p.Locale, p.TimeZone, now(), s.UserID, *in.Expected)
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	if n != 1 {
		fail(w, 409, "资料已被其他页面修改，请重新加载后再保存")
		return
	}
	if _, err = tx.Exec("UPDATE users SET name=? WHERE id=?", p.Name, s.UserID); err != nil {
		fail(w, 500, "保存失败")
		return
	}
	// The audit stores only the subject ID, never phone numbers or profile text.
	if _, err = tx.Exec("INSERT INTO audit VALUES(?,?,?,?,?,?)", id(), s.UserID, "profile.update", s.UserID, ip(r), now()); err != nil {
		fail(w, 500, "保存失败")
		return
	}
	p, err = readProfile(tx, s.UserID)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "保存失败")
		return
	}
	jsonOut(w, 200, p)
}

type Annotations struct {
	Tags      []string `json:"tags"`
	Notes     string   `json:"notes"`
	Revision  int64    `json:"revision"`
	UpdatedAt int64    `json:"updated_at"`
}

func readAnnotations(db queryRower, uid string) (Annotations, error) {
	out := Annotations{Tags: []string{}}
	var tags string
	err := db.QueryRow("SELECT tags,notes,revision,updated_at FROM user_annotations WHERE user_id=?", uid).Scan(&tags, &out.Notes, &out.Revision, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(tags), &out.Tags)
	}
	return out, err
}

func (a *App) updateAnnotations(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Tags     []string `json:"tags"`
		Notes    string   `json:"notes"`
		Expected *int64   `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Notes = strings.TrimSpace(in.Notes)
	if in.Expected == nil || *in.Expected < 0 || len(in.Tags) > 12 || len(in.Notes) > 2000 {
		fail(w, 400, "最多 12 个标签，备注最多 2000 字节，必须提供版本")
		return
	}
	seen := map[string]bool{}
	for i, tag := range in.Tags {
		tag = strings.TrimSpace(tag)
		if !validText(tag, 48) || seen[tag] || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			fail(w, 400, "标签为空、重复或过长")
			return
		}
		in.Tags[i] = tag
		seen[tag] = true
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow("SELECT 1 FROM users WHERE id=?", r.PathValue("id")).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "用户不存在")
		return
	} else if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	uid := r.PathValue("id")
	if _, err = tx.Exec("INSERT INTO user_annotations(user_id) VALUES(?) ON CONFLICT(user_id) DO NOTHING", uid); err != nil {
		fail(w, 500, "保存失败")
		return
	}
	tags, _ := json.Marshal(in.Tags)
	res, err := tx.Exec("UPDATE user_annotations SET tags=?,notes=?,revision=revision+1,updated_at=? WHERE user_id=? AND revision=?", string(tags), in.Notes, now(), uid, *in.Expected)
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		fail(w, 500, "保存失败")
		return
	}
	if n != 1 {
		fail(w, 409, "用户标签已被修改，请重新加载后保存")
		return
	}
	if _, err = tx.Exec("INSERT INTO audit VALUES(?,?,?,?,?,?)", id(), s.UserID, "user.annotate", uid, ip(r), now()); err != nil {
		fail(w, 500, "保存失败")
		return
	}
	out, err := readAnnotations(tx, uid)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "保存失败")
		return
	}
	jsonOut(w, 200, out)
}
