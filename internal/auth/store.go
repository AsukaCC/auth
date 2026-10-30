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
)

type Config struct {
	DataDir, WebDir, PublicURL string
	SecureCookie               bool
}
type App struct {
	db      *sql.DB
	cfg     Config
	cipher  cipher.AEAD
	limitMu sync.Mutex
	limits  map[string]bucket
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
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(filepath.Join(cfg.DataDir, "auth.db"), 0600)
	return &App{db: db, cfg: cfg, cipher: gcm, limits: map[string]bucket{}}, nil
}
func (a *App) Close() error { return a.db.Close() }
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
