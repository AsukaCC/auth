package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestBootstrapRequiresValidCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, email, password, displayName string
	}{
		{"missing email", "", "valid-password", "Admin"},
		{"invalid email", "not-an-email", "valid-password", "Admin"},
		{"missing password", "admin@example.com", "", "Admin"},
		{"short password", "admin@example.com", "short", "Admin"},
		{"long password", "admin@example.com", strings.Repeat("x", 73), "Admin"},
		{"multibyte password limit", "admin@example.com", strings.Repeat("密", 25), "Admin"},
		{"long name", "admin@example.com", "valid-password", strings.Repeat("x", 81)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{DataDir: t.TempDir(), AdminEmail: tc.email, AdminPassword: tc.password, AdminName: tc.displayName}
			a, err := New(cfg)
			if err == nil {
				a.Close()
				t.Fatal("invalid initialization configuration was accepted")
			}
			if tc.password != "" && strings.Contains(err.Error(), tc.password) {
				t.Fatal("initialization error exposed the password")
			}
			cfg.AdminEmail, cfg.AdminPassword, cfg.AdminName = "admin@example.com", "valid-password", "Admin"
			a, err = New(cfg)
			if err != nil {
				t.Fatalf("cannot retry after correcting configuration: %v", err)
			}
			a.Close()
		})
	}
}

func TestBootstrapAdminOnlyOnce(t *testing.T) {
	cfg := Config{DataDir: t.TempDir(), AdminEmail: "  ADMIN@Example.com ", AdminPassword: "initial-password"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var uid, digest, name, role string
	if err := a.db.QueryRow("SELECT id,password,name,role FROM users WHERE email='admin@example.com'").Scan(&uid, &digest, &name, &role); err != nil {
		t.Fatal(err)
	}
	if name != "管理员" || role != "admin" || bcrypt.CompareHashAndPassword([]byte(digest), []byte(cfg.AdminPassword)) != nil {
		t.Fatal("administrator was not initialized from configuration")
	}
	if a.cfg.AdminPassword != "" {
		t.Fatal("bootstrap password retained in application configuration")
	}
	c := &testClient{t: t, a: a}
	c.request("POST", "/api/auth/login", map[string]string{"email": "admin@example.com", "password": cfg.AdminPassword}, 200)
	c.request("POST", "/api/password", map[string]string{"current_password": cfg.AdminPassword, "password": "changed-password"}, 200)
	if _, err := a.db.Exec("UPDATE users SET name='Changed',role='user',disabled=1,totp=? WHERE id=?", a.seal("totp-must-survive"), uid); err != nil {
		t.Fatal(err)
	}
	var persistedDigest, persistedTOTP string
	if err := a.db.QueryRow("SELECT password,totp FROM users WHERE id=?", uid).Scan(&persistedDigest, &persistedTOTP); err != nil {
		t.Fatal(err)
	}
	a.Close()
	for _, cfg := range []Config{
		{DataDir: cfg.DataDir, AdminEmail: "admin@example.com", AdminPassword: "replacement-password", AdminName: "Replacement"},
		{DataDir: cfg.DataDir, AdminEmail: "another@example.com", AdminPassword: "another-password"},
		{DataDir: cfg.DataDir},
	} {
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		u, err := a.user(uid)
		if err != nil || u.Name != "Changed" || u.Role != "user" || !u.Disabled || !u.TFA {
			t.Fatal("restart overwrote an existing account")
		}
		var count int
		var currentDigest, currentTOTP string
		if err := a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 1 {
			t.Fatal("restart created another administrator")
		}
		if err := a.db.QueryRow("SELECT password,totp FROM users WHERE id=?", uid).Scan(&currentDigest, &currentTOTP); err != nil || currentDigest != persistedDigest || currentTOTP != persistedTOTP {
			t.Fatal("restart reset credentials")
		}
		a.Close()
	}
}

func TestPublicSetupEndpointRemoved(t *testing.T) {
	c := newTest(t)
	c.request("POST", "/api/setup", map[string]string{"email": "attacker@example.com", "name": "Attacker", "password": "attacker-password"}, 404)
	if _, exists := c.request("GET", "/api/status", nil, 200)["setup_required"]; exists {
		t.Fatal("public status still advertises browser initialization")
	}
	// Even an empty database must not expose a browser-based bootstrap route.
	if _, err := c.a.db.Exec("DELETE FROM users"); err != nil {
		t.Fatal(err)
	}
	c.request("POST", "/api/setup", map[string]string{"email": "attacker@example.com", "name": "Attacker", "password": "attacker-password"}, 404)
	var count int
	if err := c.a.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatal("public endpoint recreated an administrator")
	}
}
