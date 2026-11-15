package deployagent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	p "authcenter/internal/deployprotocol"
)

const maxBundleSize int64 = 2 << 30

func githubClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !allowedDownloadURL(req.URL) {
			return errors.New("下载重定向被拒绝")
		}
		req.Header.Del("Authorization")
		return nil
	}}
}
func allowedDownloadURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func (a *Agent) get(ctx context.Context, path, accept string) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+a.cfg.Repository+path, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "Auth-Center-Deploy-Agent/1")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if a.cfg.TokenFile != "" {
		b, e := os.ReadFile(a.cfg.TokenFile)
		if e != nil {
			return nil, errors.New("无法读取 GitHub 凭据文件")
		}
		value := strings.TrimSpace(string(b))
		if value == "" || strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("GitHub 凭据文件无效")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	res, e := a.client.Do(req)
	if e != nil {
		return nil, errors.New("无法连接 GitHub 发布源")
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("GitHub 发布源返回状态 %d", res.StatusCode)
	}
	return res, nil
}
func (a *Agent) check(ctx context.Context) (*p.Release, error) {
	res, e := a.get(ctx, "/releases/latest", "application/vnd.github+json")
	if e != nil {
		return nil, e
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	e = json.NewDecoder(io.LimitReader(res.Body, 1024*1024)).Decode(&release)
	res.Body.Close()
	if e != nil || release.Draft || release.Prerelease {
		return nil, errors.New("最新正式版本信息无效")
	}
	version := strings.TrimPrefix(release.Tag, "v")
	if len(version) > 80 || !versionPattern.MatchString(version) || strings.Contains(version, "-") {
		return nil, errors.New("正式发布标签必须是 v主版本.次版本.修订版本")
	}
	arch := strings.TrimPrefix(a.platform, "linux-")
	if arch != "amd64" && arch != "arm64" {
		return nil, errors.New("当前处理器架构不支持网页更新")
	}
	name := "auth-center-" + version + "-" + arch
	var manifestID, bundleID, bundleSize int64
	for _, asset := range release.Assets {
		if asset.Name == name+".update.json" {
			if manifestID != 0 {
				return nil, errors.New("发布清单重复")
			}
			manifestID = asset.ID
		}
		if asset.Name == name+".tar.gz" {
			if bundleID != 0 {
				return nil, errors.New("发布安装包重复")
			}
			bundleID = asset.ID
			bundleSize = asset.Size
		}
	}
	if manifestID <= 0 || bundleID <= 0 || bundleSize <= 0 || bundleSize > maxBundleSize {
		return nil, errors.New("该版本缺少适用架构的安装包或更新清单")
	}
	res, e = a.get(ctx, "/releases/assets/"+strconv.FormatInt(manifestID, 10), "application/octet-stream")
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	d := json.NewDecoder(io.LimitReader(res.Body, 16385))
	d.DisallowUnknownFields()
	var m p.Manifest
	if d.Decode(&m) != nil || d.Decode(&struct{}{}) != io.EOF || m.Format != 1 || m.Version != version || m.Schema < 1 || m.Platform != a.platform || m.Bundle != name+".tar.gz" || !digestPattern.MatchString(m.SHA256) || m.Size != bundleSize {
		return nil, errors.New("更新清单与 GitHub 发布资源不一致")
	}
	return &p.Release{Version: version, Schema: m.Schema, Platform: m.Platform, SHA256: m.SHA256, Size: m.Size, AssetID: bundleID}, nil
}

// Only three exact regular files may be imported. Downloaded executable scripts,
// Compose mounts, symlinks and archive paths never replace local deployment tools.
func (a *Agent) prepare(ctx context.Context, j *p.Job) (string, error) {
	r := j.Release
	if r == nil || r.Size <= 0 || r.Size > maxBundleSize || !versionPattern.MatchString(r.Version) || !digestPattern.MatchString(r.SHA256) {
		return "", errors.New("更新任务清单无效")
	}
	stage, e := os.MkdirTemp(filepath.Join(a.cfg.Root, ".deployment", "agent"), "download-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(stage)
	res, e := a.get(ctx, "/releases/assets/"+strconv.FormatInt(r.AssetID, 10), "application/octet-stream")
	if e != nil {
		return "", e
	}
	f, e := os.OpenFile(filepath.Join(stage, "bundle.tar.gz"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		res.Body.Close()
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, r.Size+1))
	res.Body.Close()
	if e != nil || n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return "", errors.New("安装包大小或 SHA-256 校验失败")
	}
	if _, e = f.Seek(0, 0); e != nil {
		return "", e
	}
	gz, e := gzip.NewReader(f)
	if e != nil {
		return "", errors.New("安装包压缩格式无效")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	base := "auth-center/packages/" + r.Version
	wanted := map[string]string{"auth-center/RELEASE": "RELEASE", base + ".meta": "image.meta", base + ".tar": "image.tar"}
	found := map[string]bool{}
	var total int64
	for count := 0; ; count++ {
		hdr, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil || count > 1000 {
			return "", errors.New("安装包归档无效")
		}
		total += hdr.Size
		if hdr.Size < 0 || total > 4<<30 {
			return "", errors.New("安装包解压大小超限")
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.Contains("/"+name+"/", "/../") {
			return "", errors.New("安装包路径无效")
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			return "", errors.New("安装包包含不允许的链接或特殊文件")
		}
		target, ok := wanted[name]
		if !ok {
			continue
		}
		if found[name] || hdr.Typeflag != tar.TypeReg || hdr.Size < 1 || (target != "image.tar" && hdr.Size > 4096) {
			return "", errors.New("安装包关键文件无效或重复")
		}
		found[name] = true
		out, e := os.OpenFile(filepath.Join(stage, target), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return "", e
		}
		_, e = io.Copy(out, tr)
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e != nil {
			return "", e
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	if len(found) != 3 {
		return "", errors.New("安装包缺少镜像或版本信息")
	}
	if _, e = io.Copy(io.Discard, io.LimitReader(gz, 1)); e != nil {
		return "", errors.New("安装包压缩校验失败")
	}
	version, e := os.ReadFile(filepath.Join(stage, "RELEASE"))
	if e != nil || strings.TrimSpace(string(version)) != r.Version {
		return "", errors.New("安装包版本不一致")
	}
	meta, e := os.ReadFile(filepath.Join(stage, "image.meta"))
	if e != nil {
		return "", e
	}
	parts := strings.Split(strings.TrimSpace(string(meta)), "\n")
	if len(parts) != 3 || !imagePattern.MatchString(parts[0]) || !digestPattern.MatchString(parts[1]) || parts[2] != strconv.Itoa(r.Schema) {
		return "", errors.New("镜像清单无效")
	}
	image, e := os.Open(filepath.Join(stage, "image.tar"))
	if e != nil {
		return "", e
	}
	h = sha256.New()
	_, e = io.Copy(h, image)
	image.Close()
	if e != nil || hex.EncodeToString(h.Sum(nil)) != parts[1] {
		return "", errors.New("镜像摘要校验失败")
	}
	packages := filepath.Join(a.cfg.Root, "packages")
	if e = os.MkdirAll(packages, 0700); e != nil {
		return "", e
	}
	// An existing version is immutable, including artifacts left by failed jobs.
	for _, file := range []struct{ src, dst string }{{"image.tar", r.Version + ".tar"}, {"image.meta", r.Version + ".meta"}} {
		src := filepath.Join(stage, file.src)
		dst := filepath.Join(packages, file.dst)
		in, e := os.Open(src)
		if e != nil {
			return "", e
		}
		// Staging is on the same filesystem. A hard link publishes the complete
		// file atomically, so a crash cannot leave a partial immutable package.
		e = os.Link(src, dst)
		if os.IsExist(e) {
			old, e := os.Open(dst)
			if e != nil {
				in.Close()
				return "", e
			}
			h1, h2 := sha256.New(), sha256.New()
			_, e1 := io.Copy(h1, in)
			_, e2 := io.Copy(h2, old)
			in.Close()
			old.Close()
			if e1 != nil || e2 != nil || hex.EncodeToString(h1.Sum(nil)) != hex.EncodeToString(h2.Sum(nil)) {
				return "", errors.New("同一版本已存在不同内容，拒绝覆盖")
			}
			continue
		}
		if e != nil {
			in.Close()
			return "", e
		}
		in.Close()
	}
	return parts[0], nil
}

func newer(candidate, current string) bool {
	if !versionPattern.MatchString(candidate) {
		return false
	}
	if !versionPattern.MatchString(current) {
		return true
	}
	c := strings.SplitN(candidate, "-", 2)
	v := strings.SplitN(current, "-", 2)
	left, right := strings.Split(c[0], "."), strings.Split(v[0], ".")
	for i := 0; i < 3; i++ {
		x, y := strings.TrimLeft(left[i], "0"), strings.TrimLeft(right[i], "0")
		if len(x) != len(y) {
			return len(x) > len(y)
		}
		if x != y {
			return x > y
		}
	}
	return len(c) == 1 && len(v) == 2
}
