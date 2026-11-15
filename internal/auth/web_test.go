package auth

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSiteMetadata(t *testing.T) {
	a := &App{cfg: Config{WebDir: t.TempDir(), PublicURL: "https://auth.example.test/"}}
	// Exercise the real source document; the production build retains this marker.
	source, err := os.ReadFile("../../web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(a.cfg.WebDir, "index.html"), source, 0600); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	for _, tc := range []struct {
		name, target string
		cookie       bool
		private      bool
	}{
		{"public", "/", false, false}, {"recovery", "/?reset=secret-recovery-token", false, true},
		{"console", "/", true, true}, {"duplicate", "/index.html", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			r.Host = "untrusted.example"
			r.Header.Set("X-Forwarded-Host", "injected.example")
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: "auth_session", Value: "private-session"})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d", w.Code)
			}
			body := w.Body.String()
			for _, want := range []string{`rel="canonical" href="https://auth.example.test/"`, `property="og:image" content="https://auth.example.test/social-preview.png"`, `rel="apple-touch-icon"`, `rel="manifest"`, `name="description"`} {
				if !strings.Contains(body, want) {
					t.Errorf("missing metadata %s", want)
				}
			}
			for _, secret := range []string{"untrusted.example", "injected.example", "secret-recovery-token", "private-session", "<!-- auth:seo -->"} {
				if strings.Contains(body, secret) {
					t.Errorf("metadata leaked %s", secret)
				}
			}
			if strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") != tc.private {
				t.Error("wrong indexing header")
			}
			if strings.Contains(body, `name="robots" content="noindex`) != tc.private {
				t.Error("wrong HTML robots")
			}
			schema := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(body)
			if len(schema) != 2 {
				t.Fatal("missing structured data")
			}
			var data map[string]any
			if json.Unmarshal([]byte(schema[1]), &data) != nil || data["url"] != "https://auth.example.test/" {
				t.Fatal("invalid structured data")
			}
		})
	}
	for _, path := range []string{"/missing", "/missing.png", "/assets/missing.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 || strings.Contains(w.Body.String(), "<html") {
			t.Fatalf("soft 404 at %s", path)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("HEAD", "/", nil))
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("HEAD response invalid")
	}
}

func TestCrawlerFiles(t *testing.T) {
	a := &App{cfg: Config{PublicURL: "https://auth.example.test"}}
	h := a.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/robots.txt", nil))
	for _, want := range []string{"Disallow: /api/", "Disallow: /v1/", "Disallow: /*?", "Sitemap: https://auth.example.test/sitemap.xml"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("missing crawler rule %s", want)
		}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/sitemap.xml?reset=secret", nil))
	var sitemap struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if xml.Unmarshal(w.Body.Bytes(), &sitemap) != nil || len(sitemap.URLs) != 1 || sitemap.URLs[0].Loc != "https://auth.example.test/" {
		t.Fatal("sitemap exposes non-public URLs")
	}
	if w.Header().Get("X-Robots-Tag") == "" {
		t.Error("query must not be indexed")
	}
	for _, invalid := range []string{"", "javascript:alert(1)", "https://user:password@auth.example.test", "https://auth.example.test/?reset=secret", "https://auth.example.test/subpath"} {
		a.cfg.PublicURL = invalid
		if a.siteURL() != "" {
			t.Errorf("invalid canonical base accepted: %s", invalid)
		}
	}
}
