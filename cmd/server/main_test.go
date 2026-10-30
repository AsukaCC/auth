package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDotEnvLoading(t *testing.T) {
	key := "AUTH_TEST_DOTENV_VALUE"
	t.Setenv(key, "restore-after-test")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(key+"='literal$hash# value'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err != nil || os.Getenv(key) != "literal$hash# value" {
		t.Fatal("dotenv did not preserve quoted values")
	}
	t.Setenv(key, "process-value")
	if err := loadDotEnv(path); err != nil || os.Getenv(key) != "process-value" {
		t.Fatal("dotenv overwrote the process environment")
	}
	if err := loadDotEnv(filepath.Join(t.TempDir(), ".env")); err != nil {
		t.Fatal("missing optional dotenv file must be allowed")
	}
	if err := os.WriteFile(path, []byte("AUTH_TEST_DOTENV_VALUE='private-unclosed-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err == nil || strings.Contains(err.Error(), "private-unclosed-password") {
		t.Fatal("invalid dotenv must fail without exposing its contents")
	}
}
