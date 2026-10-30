package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

type Config struct {
	DataDir, WebDir, PublicURL           string
	SecureCookie                         bool
	AdminEmail, AdminName, AdminPassword string
}
type App struct {
	db        *sql.DB
	cfg       Config
	cipher    cipher.AEAD
	limitMu   sync.Mutex
	limits    map[string]bucket
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}
type bucket struct {
	Count int
	Start time.Time
}
type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
	TFA      bool   `json:"tfa"`
	Created  int64  `json:"created_at"`
}

const schema = `
PRAGMA foreign_keys=ON;
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,email TEXT NOT NULL COLLATE NOCASE UNIQUE,name TEXT NOT NULL,password TEXT NOT NULL,role TEXT NOT NULL DEFAULT 'user',disabled INTEGER NOT NULL DEFAULT 0,totp TEXT NOT NULL DEFAULT '',pending_totp TEXT NOT NULL DEFAULT '',last_totp INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS applications(id TEXT PRIMARY KEY,name TEXT NOT NULL,description TEXT NOT NULL,owner_id TEXT NOT NULL REFERENCES users(id),created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS api_keys(id TEXT PRIMARY KEY,app_id TEXT NOT NULL REFERENCES applications(id),name TEXT NOT NULL,prefix TEXT NOT NULL,hash TEXT NOT NULL UNIQUE,scopes TEXT NOT NULL,created_at INTEGER NOT NULL,last_used INTEGER NOT NULL DEFAULT 0,revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),app_id TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,ip TEXT NOT NULL,agent TEXT NOT NULL,last_seen INTEGER NOT NULL,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS recovery(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id),hash TEXT NOT NULL UNIQUE,expires_at INTEGER NOT NULL,used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS appeals(id TEXT PRIMARY KEY,email TEXT NOT NULL,contact TEXT NOT NULL,reason TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',note TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL,resolved_at INTEGER NOT NULL DEFAULT 0);
CREATE UNIQUE INDEX IF NOT EXISTS pending_appeal ON appeals(email COLLATE NOCASE) WHERE status='pending';
CREATE TABLE IF NOT EXISTS backup_codes(user_id TEXT NOT NULL REFERENCES users(id),hash TEXT NOT NULL UNIQUE);
CREATE TABLE IF NOT EXISTS audit(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,action TEXT NOT NULL,detail TEXT NOT NULL,ip TEXT NOT NULL,created_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS session_user ON sessions(user_id);
`

func New(cfg Config) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	// Lock before reading or creating the key/database. The OS releases it on exit;
	// never unlink the lock file, which would allow locking a different inode.
	lock, err := os.OpenFile(filepath.Join(cfg.DataDir, ".instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("数据目录已被其他实例占用，禁止同时运行多个实例")
	}
	opened := false
	defer func() {
		if !opened {
			lock.Close()
		}
	}()
	keyPath := filepath.Join(cfg.DataDir, "master.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, e
		}
		_, err = f.Write(key)
		f.Close()
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid master key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "auth.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("数据库版本 %d 高于当前程序支持的 %d，拒绝不兼容回退", version, SchemaVersion)
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Existing version-0 databases use exactly this schema; no data rewrite.
	if version == 0 {
		if _, err = db.Exec("PRAGMA user_version=1"); err != nil {
			db.Close()
			return nil, err
		}
	}
	_ = os.Chmod(filepath.Join(cfg.DataDir, "auth.db"), 0600)
	password := cfg.AdminPassword
	cfg.AdminPassword = ""
	a := &App{db: db, cfg: cfg, cipher: gcm, limits: map[string]bucket{}, lock: lock}
	if err := a.initializeAdmin(cfg.AdminEmail, cfg.AdminName, password); err != nil {
		db.Close()
		return nil, err
	}
	opened = true
	return a, nil
}

func (a *App) initializeAdmin(email, name, password string) error {
	var count int
	if err := a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("check administrator initialization: %w", err)
	}
	// Initialization never resets or promotes accounts in an existing database.
	if count > 0 {
		return nil
	}
	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if name == "" {
		name = "管理员"
	}
	if !emailValid(email) {
		return errors.New("首次启动必须配置有效的 AUTH_ADMIN_EMAIL")
	}
	if !validText(name, 80) {
		return errors.New("AUTH_ADMIN_NAME 不能超过 80 字节")
	}
	if err := checkPassword(password); err != nil {
		return errors.New("首次启动必须配置 AUTH_ADMIN_PASSWORD（10–72 字节）")
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("无法生成管理员密码摘要")
	}
	_, err = a.db.Exec("INSERT INTO users(id,email,name,password,role,created_at) SELECT ?,?,?,?,'admin',? WHERE NOT EXISTS(SELECT 1 FROM users)", id(), email, name, string(digest), now())
	if err != nil {
		return errors.New("初始化管理员失败")
	}
	return nil
}
func (a *App) Close() error {
	a.closeOnce.Do(func() { a.closeErr = errors.Join(a.db.Close(), a.lock.Close()) })
	return a.closeErr
}
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func id() string           { return token()[:22] }
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func now() int64           { return time.Now().Unix() }
func (a *App) seal(s string) string {
	if s == "" {
		return ""
	}
	n := make([]byte, a.cipher.NonceSize())
	if _, err := rand.Read(n); err != nil {
		panic(err)
	}
	return base64.RawStdEncoding.EncodeToString(a.cipher.Seal(n, n, []byte(s), nil))
}
func (a *App) open(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, e := base64.RawStdEncoding.DecodeString(s)
	if e != nil || len(b) < a.cipher.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	v, e := a.cipher.Open(nil, b[:a.cipher.NonceSize()], b[a.cipher.NonceSize():], nil)
	return string(v), e
}
func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var secret string
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Disabled, &secret, &u.Created)
	u.TFA = secret != ""
	return u, err
}

const userColumns = "id,email,name,role,disabled,totp,created_at"

func (a *App) user(uid string) (User, error) {
	return scanUser(a.db.QueryRow("SELECT "+userColumns+" FROM users WHERE id=?", uid))
}
func (a *App) audit(uid, action, detail, ip string) {
	_, _ = a.db.Exec("INSERT INTO audit VALUES(?,?,?,?,?,?)", id(), uid, action, detail, ip, now())
}
func validText(s string, n int) bool { return len(strings.TrimSpace(s)) > 0 && len(s) <= n }
func checkPassword(p string) error {
	if len(p) < 10 || len(p) > 72 {
		return fmt.Errorf("密码需要 10–72 个字节")
	}
	return nil
}
