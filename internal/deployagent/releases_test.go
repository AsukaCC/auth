package deployagent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	p "authcenter/internal/deployprotocol"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func sum(b []byte) string                                             { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func bundle(t *testing.T, version, extra string, link bool) ([]byte, []byte) {
	t.Helper()
	image := []byte("test-image-" + version)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	files := map[string][]byte{
		"auth-center/RELEASE":                       []byte(version + "\n"),
		"auth-center/packages/" + version + ".tar":  image,
		"auth-center/packages/" + version + ".meta": []byte(previousID + "\n" + sum(image) + "\n2\n"),
		"auth-center/authctl.sh":                    []byte("untrusted script must never execute"),
	}
	if extra != "" {
		files[extra] = []byte("bad")
	}
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if name == extra && link {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = "/etc/passwd"
			hdr.Size = 0
			data = nil
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), image
}

func TestBundleValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path    string
		link, corrupt bool
	}{
		{name: "valid"}, {name: "traversal", path: "auth-center/../../escape"},
		{name: "symlink", path: "auth-center/link", link: true}, {name: "digest", corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAgent(t)
			data, image := bundle(t, "1.2.1", tc.path, tc.link)
			checksum := sum(data)
			if tc.corrupt {
				checksum = strings.Repeat("0", 64)
			}
			a.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.github.com" || !strings.HasSuffix(r.URL.Path, "/releases/assets/12") {
					t.Error("unexpected download")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}
			j := &p.Job{Release: &p.Release{Version: "1.2.1", Schema: 2, Size: int64(len(data)), SHA256: checksum, AssetID: 12}}
			id, err := a.prepare(context.Background(), j)
			if tc.name != "valid" {
				if err == nil {
					t.Fatal("invalid archive accepted")
				}
				return
			}
			if err != nil || id != previousID {
				t.Fatalf("prepare %s %v", id, err)
			}
			installed, err := os.ReadFile(filepath.Join(a.cfg.Root, "packages", "1.2.1.tar"))
			if err != nil || !bytes.Equal(installed, image) {
				t.Fatal("image not imported")
			}
			script, _ := os.ReadFile(filepath.Join(a.cfg.Root, "authctl.sh"))
			if string(script) != "test" {
				t.Fatal("remote executable replaced local script")
			}
			if _, err = a.prepare(context.Background(), j); err != nil {
				t.Fatal("identical retry rejected", err)
			}
			if err = os.WriteFile(filepath.Join(a.cfg.Root, "packages", "1.2.1.tar"), []byte("different"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = a.prepare(context.Background(), j); err == nil {
				t.Fatal("immutable version overwritten")
			}
		})
	}
}

func TestReleaseManifest(t *testing.T) {
	a := testAgent(t)
	a.platform = "linux-amd64"
	a.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"tag_name":"v1.2.1","assets":[{"id":1,"name":"auth-center-1.2.1-amd64.update.json","size":300},{"id":2,"name":"auth-center-1.2.1-amd64.tar.gz","size":100}]}`
		if strings.HasSuffix(r.URL.Path, "/assets/1") {
			body = fmt.Sprintf(`{"format":1,"version":"1.2.1","schema":2,"platform":"linux-amd64","bundle":"auth-center-1.2.1-amd64.tar.gz","sha256":"%s","size":100}`, strings.Repeat("a", 64))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	release, err := a.check(context.Background())
	if err != nil || release.AssetID != 2 || release.Version != "1.2.1" {
		t.Fatalf("check %+v %v", release, err)
	}
	for _, raw := range []string{"http://github.com/a", "https://github.com.evil.test/a", "https://127.0.0.1/a", "https://github.com:8443/a", "https://u:p@github.com/a"} {
		u, _ := url.Parse(raw)
		if allowedDownloadURL(u) {
			t.Fatalf("unsafe redirect accepted: %s", raw)
		}
	}
	for _, v := range []struct {
		candidate, current string
		want               bool
	}{
		{"1.10.0", "1.9.0", true}, {"1.2.0", "1.2.0", false}, {"1.2.0", "1.2.0-rc.1", true}, {"1.1.0", "1.2.0", false},
	} {
		if newer(v.candidate, v.current) != v.want {
			t.Fatalf("incorrect version ordering %+v", v)
		}
	}
}

func TestUpdateJob(t *testing.T) {
	a := testAgent(t)
	data, _ := bundle(t, "1.2.1", "", false)
	a.release = &p.Release{Version: "1.2.1", Schema: 2, Platform: a.platform, SHA256: sum(data), Size: int64(len(data)), AssetID: 12}
	a.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}
	var mu sync.Mutex
	switched := false
	a.run = func(ctx context.Context, root string, args, env []string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if args[0] == "sh" {
			if args[2] != "update" {
				t.Error("wrong deployment action")
			}
			if !strings.Contains(strings.Join(env, "\n"), "AUTH_EXPECT_TARGET_ID="+previousID) {
				t.Error("missing immutable target guard")
			}
			if !strings.Contains(strings.Join(env, "\n"), "AUTH_TARGET_VERSION=1.2.1") {
				t.Error("missing release selector")
			}
			if _, err := os.Stat(filepath.Join(root, "packages", "1.2.1.tar")); err != nil {
				t.Error("deployment started before download validation")
			}
			switched = true
			return nil, os.WriteFile(filepath.Join(root, ".deployment", "state"), []byte("1.2.1\n"+previousID+"\n1.2.0\n"+currentID+"\n"), 0600)
		}
		switch args[1] {
		case "ps":
			return []byte("container\n"), nil
		case "inspect":
			id := currentID
			if switched {
				id = previousID
			}
			return []byte(id + "\ntrue\nhealthy\nauth-center_auth-data\n"), nil
		case "image":
			version := "1.2.0"
			if args[len(args)-1] == previousID {
				version = "1.2.1"
			}
			return []byte(version + "\n2\n"), nil
		}
		return nil, fmt.Errorf("unexpected command")
	}
	if err := os.WriteFile(filepath.Join(a.cfg.Root, ".deployment", "state"), []byte("1.2.0\n"+currentID+"\n\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := p.Request{Action: "update", ExpectedImage: currentID, Version: "1.2.1", SHA256: strings.Repeat("0", 64), IdempotencyKey: "update-request-123", Actor: "admin"}
	if _, code, _ := a.enqueue(context.Background(), request); code != 409 {
		t.Fatal("unreviewed release digest accepted")
	}
	request.SHA256 = a.release.SHA256
	if _, code, err := a.enqueue(context.Background(), request); err != nil || code != 202 {
		t.Fatalf("update rejected: %d %v", code, err)
	}
	result := waitJob(t, a)
	if result.Status != "succeeded" {
		t.Fatalf("update failed: %+v", result)
	}
}
