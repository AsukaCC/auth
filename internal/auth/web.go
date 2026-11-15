package auth

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Use only the configured origin. Host/forwarded headers and recovery query
// strings must never become canonical URLs, social metadata or sitemap entries.
func (a *App) siteURL() string {
	u, err := url.Parse(a.cfg.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/"
}

func (a *App) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\nAllow: /$\nDisallow: /api/\nDisallow: /v1/\nDisallow: /health\nDisallow: /*?\n")
	if site := a.siteURL(); site != "" {
		fmt.Fprintf(w, "\nSitemap: %ssitemap.xml\n", site)
	}
}

func (a *App) sitemap(w http.ResponseWriter, r *http.Request) {
	site := a.siteURL()
	if site == "" {
		http.Error(w, "请配置有效的 AUTH_PUBLIC_URL", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, xml.Header+`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>`)
	_ = xml.EscapeText(w, []byte(site))
	fmt.Fprint(w, `</loc></url></urlset>`)
}

func (a *App) web(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
		fail(w, 404, "接口不存在")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "请求方法不受支持", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		file := filepath.Join(a.cfg.WebDir, filepath.Clean("/"+r.URL.Path))
		st, err := os.Stat(file)
		if err != nil || !st.Mode().IsRegular() {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/site.webmanifest" {
			w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		}
		http.ServeFile(w, r, file)
		return
	}
	content, err := os.ReadFile(filepath.Join(a.cfg.WebDir, "index.html"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	private := r.URL.RawQuery != "" || r.URL.Path != "/"
	if cookie, err := r.Cookie("auth_session"); err == nil && cookie.Value != "" {
		private = true
	}
	if private {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		content = bytes.ReplaceAll(content, []byte(`name="robots" content="index, follow"`), []byte(`name="robots" content="noindex, nofollow, noarchive"`))
	}
	var meta strings.Builder
	if site := a.siteURL(); site != "" {
		escaped := html.EscapeString(site)
		image := html.EscapeString(site + "social-preview.png")
		fmt.Fprintf(&meta, `<link rel="canonical" href="%s" />
    <meta property="og:url" content="%s" />
    <meta property="og:image" content="%s" />
    <meta property="og:image:type" content="image/png" />
    <meta name="twitter:image" content="%s" />`, escaped, escaped, image, image)
		schema, _ := json.Marshal(map[string]any{
			"@context": "https://schema.org", "@type": "WebApplication", "name": "Auth Center",
			"url": site, "description": "统一身份与访问管理平台，集中管理账户、应用、登录会话与访问权限。",
			"applicationCategory": "SecurityApplication", "operatingSystem": "Web", "inLanguage": "zh-CN", "image": site + "social-preview.png",
		})
		// json.Marshal escapes HTML characters, including any closing script tag.
		fmt.Fprintf(&meta, "\n    <script type=\"application/ld+json\">%s</script>", schema)
	}
	content = bytes.ReplaceAll(content, []byte("<!-- auth:seo -->"), []byte(meta.String()))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(content))
}
