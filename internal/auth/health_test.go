package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicHealth(t *testing.T) {
	c := newTest(t)
	h := c.a.Handler()
	for _, unavailable := range []bool{false, true} {
		state := "healthy"
		if unavailable {
			state = "unavailable"
			if err := c.a.db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"/health", "/api/health", "/healthz"} {
			t.Run(path+"/"+state, func(t *testing.T) {
				// No console cookie, API key, or Bearer token is required.
				r := httptest.NewRequest(http.MethodGet, path, nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				wantStatus := http.StatusOK
				wantBody := map[string]string{"status": "ok"}
				if unavailable {
					wantStatus = http.StatusServiceUnavailable
					wantBody = map[string]string{"error": "服务暂不可用"}
				} else if path == "/healthz" {
					wantStatus = http.StatusNoContent
				}
				if w.Code != wantStatus {
					t.Fatalf("status = %d, want %d: %s", w.Code, wantStatus, w.Body.String())
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("health response must not be cached")
				}
				if wantStatus == http.StatusNoContent {
					if w.Body.Len() != 0 {
						t.Fatal("healthz success must have no response body")
					}
					return
				}
				if w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
					t.Fatal("expected JSON response")
				}
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if len(body) != len(wantBody) {
					t.Fatalf("unexpected response fields: %v", body)
				}
				for key, value := range wantBody {
					if body[key] != value {
						t.Fatalf("response = %v, want %v", body, wantBody)
					}
				}
			})
		}
	}
}
