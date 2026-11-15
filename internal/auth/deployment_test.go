package auth

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	p "authcenter/internal/deployprotocol"
	"github.com/pquerna/otp/totp"
)

func TestDeployAPI(t *testing.T) {
	c := newTest(t)
	c.request("GET", "/api/admin/deployment", nil, 401)
	uid := c.setup()
	if c.request("GET", "/api/admin/deployment", nil, 200)["enabled"] != false {
		t.Fatal("agent enabled by default")
	}
	socket := filepath.Join(t.TempDir(), "s")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs" {
			t.Errorf("unexpected agent path %s", r.URL.Path)
		}
		var in p.Request
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Actor != uid {
			t.Error("actor missing")
		}
		if in.Action != "update" {
			t.Error("wrong operation")
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"id":"job","status":"queued"}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	c.a.cfg.DeploySocket = socket
	body := map[string]any{"action": "update", "version": "1.2.1", "expected_image": "sha256:" + strings.Repeat("a", 64), "sha256": strings.Repeat("b", 64), "idempotency_key": "test-deploy-key-123", "password": "wrong"}
	c.request("POST", "/api/admin/deployment", body, 403)
	body["password"] = "admin-password-123"
	body["action"] = "shell"
	c.request("POST", "/api/admin/deployment", body, 400)
	body["action"] = "update"
	// Roles are enforced on the server even with an existing console cookie.
	for _, role := range []string{"user", "developer"} {
		if _, err = c.a.db.Exec("UPDATE users SET role=? WHERE id=?", role, uid); err != nil {
			t.Fatal(err)
		}
		c.request("GET", "/api/admin/deployment", nil, 403)
		c.request("POST", "/api/admin/deployment", body, 403)
	}
	_, _ = c.a.db.Exec("UPDATE users SET role='admin' WHERE id=?", uid)
	cookie := c.cookie
	c.cookie = nil
	c.bearer = cookie.Value
	c.request("POST", "/api/admin/deployment", body, 401)
	c.cookie = cookie
	c.bearer = ""
	c.request("POST", "/api/admin/deployment", body, 202)
	b := c.request("POST", "/api/2fa/setup", map[string]string{"password": "admin-password-123"}, 200)
	code, _ := totp.GenerateCode(b["secret"].(string), time.Now())
	b = c.request("POST", "/api/2fa/enable", map[string]string{"code": code}, 200)
	backups := b["backup_codes"].([]any)
	c.request("POST", "/api/admin/deployment", body, 403)
	body["code"] = code
	c.request("POST", "/api/admin/deployment", body, 403) // activation code cannot be replayed
	body["code"] = backups[0].(string)
	c.request("POST", "/api/admin/deployment", body, 202)
	c.request("POST", "/api/admin/deployment", body, 403) // backup code is single use
	fresh, _ := totp.GenerateCode(bSecret(t, c, uid), time.Now().Add(30*time.Second))
	body["code"] = fresh
	c.request("POST", "/api/admin/deployment", body, 202)
	c.request("POST", "/api/admin/deployment", body, 403)
	if calls.Load() != 3 {
		t.Fatalf("unauthorized requests reached agent: %d", calls.Load())
	}
	var detail string
	if err = c.a.db.QueryRow("SELECT detail FROM audit WHERE action='deployment.request' LIMIT 1").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, "admin-password") || strings.Contains(detail, fresh) {
		t.Fatal("credential in audit")
	}
}

func bSecret(t *testing.T, c *testClient, uid string) string {
	t.Helper()
	var enc string
	if err := c.a.db.QueryRow("SELECT totp FROM users WHERE id=?", uid).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	secret, err := c.a.open(enc)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}
