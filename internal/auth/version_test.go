package auth

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestDataDirectoryLock(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), AdminEmail: "admin@example.com", AdminPassword: "test-password"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	key, _ := os.ReadFile(filepath.Join(cfg.DataDir, "master.key"))
	if second, err := New(cfg); err == nil {
		second.Close()
		t.Fatal("two instances acquired same data directory")
	}
	a.Close()
	a.Close() // Repeated cleanup must not unlock a later instance.
	b, err := New(Config{DataDir: cfg.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	keyAfter, _ := os.ReadFile(filepath.Join(cfg.DataDir, "master.key"))
	if string(key) != string(keyAfter) {
		t.Fatal("master key changed on restart")
	}
	if second, err := New(cfg); err == nil {
		second.Close()
		t.Fatal("lock was released by duplicate close")
	}
}

func TestSchemaVersionCompatibility(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), AdminEmail: "admin@example.com", AdminPassword: "test-password"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var uid string
	if err := a.db.QueryRow("SELECT id FROM users").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	// Simulate a database created before schema version tracking was introduced.
	if _, err := a.db.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	a, err = New(Config{DataDir: cfg.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := a.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("legacy database not versioned")
	}
	if _, err := a.user(uid); err != nil {
		t.Fatal("legacy user lost")
	}
	if _, err := a.db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if newer, err := New(cfg); err == nil {
		newer.Close()
		t.Fatal("incompatible downgrade accepted")
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 999 {
		t.Fatal("incompatible database modified")
	}
}

func TestStatusVersion(t *testing.T) {
	c := newTest(t)
	v := c.request("GET", "/api/status", nil, 200)
	if v["version"] != Version || v["schema_version"] != float64(SchemaVersion) {
		t.Fatalf("missing version metadata: %v", v)
	}
}
