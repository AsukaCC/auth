// Package deployagent runs outside the Auth container and never opens its database.
package deployagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	p "authcenter/internal/deployprotocol"
	"golang.org/x/sys/unix"
)

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var imagePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,128}$`)
var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type Config struct {
	Root, Socket, Repository, TokenFile string
	Project                             string
	SocketUID                           int
}

type Agent struct {
	cfg       Config
	project   string
	platform  string
	client    *http.Client
	lock      *os.File
	mu        sync.Mutex
	checking  bool
	release   *p.Release
	jobs      map[string]*p.Job
	last      *p.Job
	active    bool
	closing   bool
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
	run       func(context.Context, string, []string, []string) ([]byte, error)
}

func New(cfg Config) (*Agent, error) {
	root, err := filepath.Abs(cfg.Root)
	if err != nil || cfg.Root == "" || !repoPattern.MatchString(cfg.Repository) || cfg.Socket == "" {
		return nil, errors.New("部署代理配置无效")
	}
	cfg.Root = root
	for _, name := range []string{"compose.yaml", "authctl.sh", ".env"} {
		if st, e := os.Stat(filepath.Join(root, name)); e != nil || !st.Mode().IsRegular() {
			return nil, errors.New("安装目录缺少配置或发布工具")
		}
	}
	state := filepath.Join(root, ".deployment", "agent")
	if err = os.MkdirAll(filepath.Join(state, "jobs"), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(state, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("部署代理已在运行")
	}
	a := &Agent{cfg: cfg, project: "auth-center", platform: "linux-" + runtime.GOARCH, lock: lock, jobs: map[string]*p.Job{}, run: runCommand, client: githubClient()}
	if cfg.Project != "" {
		a.project = cfg.Project
	}
	if b, e := os.ReadFile(filepath.Join(root, ".deployment", "project")); e == nil {
		a.project = strings.TrimSpace(string(b))
	} else if !os.IsNotExist(e) {
		lock.Close()
		return nil, e
	}
	if !projectPattern.MatchString(a.project) {
		lock.Close()
		return nil, errors.New("部署项目名无效")
	}
	files, err := os.ReadDir(filepath.Join(state, "jobs"))
	if err != nil {
		lock.Close()
		return nil, err
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		b, e := os.ReadFile(filepath.Join(state, "jobs", f.Name()))
		var j p.Job
		if e != nil || json.Unmarshal(b, &j) != nil || f.Name() != j.ID+".json" || !keyPattern.MatchString(j.ID) {
			lock.Close()
			return nil, errors.New("部署任务记录损坏，拒绝忽略")
		}
		if j.Status == "running" || j.Status == "queued" {
			j.Status = "interrupted"
			j.Stage = "interrupted"
			j.Message = "代理曾中断，请检查部署状态后恢复"
			j.UpdatedAt = time.Now().Unix()
			if e = a.save(&j); e != nil {
				lock.Close()
				return nil, e
			}
		}
		a.jobs[j.ID] = &j
		if a.last == nil || j.CreatedAt > a.last.CreatedAt || (j.CreatedAt == a.last.CreatedAt && j.ID > a.last.ID) {
			a.last = &j
		}
	}
	if b, e := os.ReadFile(filepath.Join(state, "last")); e == nil {
		if j := a.jobs[strings.TrimSpace(string(b))]; j != nil {
			a.last = j
		} else {
			lock.Close()
			return nil, errors.New("最近部署任务记录无效")
		}
	} else if !os.IsNotExist(e) {
		lock.Close()
		return nil, errors.New("最近部署任务索引无法读取")
	}
	return a, nil
}

func (a *Agent) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closing = true
		a.mu.Unlock()
		a.wg.Wait()
		a.closeErr = a.lock.Close()
	})
	return a.closeErr
}

// Listen never unlinks an unrecognized socket or lock file. The agent instance
// lock is held before replacing its own stale Unix socket.
func (a *Agent) Listen() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(a.cfg.Socket), 0750); err != nil {
		return nil, err
	}
	if a.cfg.SocketUID > 0 {
		if err := os.Chown(filepath.Dir(a.cfg.Socket), 0, a.cfg.SocketUID); err != nil {
			return nil, err
		}
		if err := os.Chmod(filepath.Dir(a.cfg.Socket), 0750); err != nil {
			return nil, err
		}
	}
	if st, err := os.Lstat(a.cfg.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("代理 socket 路径被普通文件占用")
		}
		c, e := net.DialTimeout("unix", a.cfg.Socket, time.Second)
		if e == nil {
			c.Close()
			return nil, errors.New("代理 socket 已有监听者")
		}
		if !errors.Is(e, syscall.ECONNREFUSED) {
			return nil, errors.New("无法确认旧代理 socket 已停止")
		}
		if err = os.Remove(a.cfg.Socket); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	l, err := net.Listen("unix", a.cfg.Socket)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(a.cfg.Socket, 0600); err != nil {
		l.Close()
		return nil, err
	}
	if a.cfg.SocketUID > 0 {
		if err = os.Chown(a.cfg.Socket, a.cfg.SocketUID, a.cfg.SocketUID); err != nil {
			l.Close()
			return nil, err
		}
	}
	return l, nil
}

func atomicFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (a *Agent) save(j *p.Job) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	return atomicFile(filepath.Join(a.cfg.Root, ".deployment", "agent", "jobs", j.ID+".json"), b)
}
func (a *Agent) stage(j *p.Job, status, stage, message string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	j.Status = status
	j.Stage = stage
	j.Message = message
	j.UpdatedAt = time.Now().Unix()
	if err := a.save(j); err != nil {
		j.Status = "interrupted"
		j.Stage = "interrupted"
		j.Message = "无法保存任务状态，已停止后续操作；请核对服务器部署状态"
		return err
	}
	return nil
}
func cloneJob(j *p.Job) *p.Job {
	if j == nil {
		return nil
	}
	v := *j
	return &v
}

type boundedWriter struct {
	b     []byte
	limit int
}

func (w *boundedWriter) Write(b []byte) (int, error) {
	n := len(b)
	remaining := w.limit - len(w.b)
	if remaining > 0 {
		if len(b) > remaining {
			b = b[:remaining]
		}
		w.b = append(w.b, b...)
	}
	return n, nil
}
func runCommand(ctx context.Context, root string, args, env []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("missing command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	// Keep the shell and Docker CLI in one process group. On timeout the shell
	// can run its recovery trap, then the entire group is terminated if stuck.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 35 * time.Second
	out := &boundedWriter{limit: 1024 * 1024}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return out.b, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	err := cmd.Wait()
	close(done)
	return out.b, err
}
func (a *Agent) docker(ctx context.Context, args ...string) ([]byte, error) {
	return a.run(ctx, a.cfg.Root, append([]string{"docker"}, args...), nil)
}
func (a *Agent) image(ctx context.Context, id string) (*p.Image, error) {
	if !imagePattern.MatchString(id) {
		return nil, errors.New("镜像记录无效")
	}
	b, e := a.docker(ctx, "image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.version"}}{{println}}{{index .Config.Labels "io.authcenter.schema-version"}}`, id)
	if e != nil {
		return nil, errors.New("无法检查镜像")
	}
	parts := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(parts) != 2 {
		return nil, errors.New("镜像缺少版本信息")
	}
	schema, e := strconv.Atoi(strings.TrimSpace(parts[1]))
	if e != nil || schema < 1 {
		return nil, errors.New("镜像结构版本无效")
	}
	return &p.Image{ID: id, Version: strings.TrimSpace(parts[0]), Schema: schema}, nil
}
func (a *Agent) status(ctx context.Context) (p.Status, error) {
	out := p.Status{Enabled: true, Connected: true, Repository: a.cfg.Repository, Platform: a.platform}
	a.mu.Lock()
	out.Job = cloneJob(a.last)
	if a.release != nil {
		v := *a.release
		out.Release = &v
	}
	out.Locked = a.active
	a.mu.Unlock()
	_, err := os.Stat(filepath.Join(a.cfg.Root, ".deployment", "pending"))
	out.Pending = err == nil
	if err != nil && !os.IsNotExist(err) {
		return out, errors.New("无法读取迁移状态")
	}
	_, err = os.Stat(filepath.Join(a.cfg.Root, ".deployment", "operation.lock"))
	out.Locked = out.Locked || err == nil
	if err != nil && !os.IsNotExist(err) {
		return out, errors.New("无法读取发布锁")
	}
	b, err := a.docker(ctx, "ps", "-aq", "--filter", "label=com.docker.compose.project="+a.project, "--filter", "label=com.docker.compose.service=auth")
	if err != nil {
		return out, errors.New("Docker 不可用")
	}
	ids := strings.Fields(string(b))
	if len(ids) != 1 {
		return out, errors.New("必须存在且仅存在一个 Auth 容器")
	}
	b, err = a.docker(ctx, "inspect", "--format", `{{.Image}}{{println}}{{.State.Running}}{{println}}{{if .State.Health}}{{.State.Health.Status}}{{end}}{{println}}{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}`, ids[0])
	if err != nil {
		return out, errors.New("无法检查运行容器")
	}
	parts := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(parts) != 4 || parts[3] != a.project+"_auth-data" {
		return out, errors.New("容器数据卷不符合单实例部署约定")
	}
	out.Current, err = a.image(ctx, parts[0])
	if err != nil {
		return out, err
	}
	out.Current.Health = parts[2]
	if parts[1] != "true" {
		out.Current.Health = "stopped"
	}
	state, e := os.ReadFile(filepath.Join(a.cfg.Root, ".deployment", "state"))
	if e == nil {
		lines := strings.Split(strings.TrimRight(string(state), "\n"), "\n")
		if len(lines) < 2 || lines[0] == "" || !imagePattern.MatchString(lines[1]) {
			return out, errors.New("发布记录无效")
		}
		if lines[1] != out.Current.ID && !out.Pending {
			return out, errors.New("运行镜像与发布记录不一致，请先恢复或核对部署")
		}
		out.Managed = true
		if len(lines) >= 4 && lines[3] != "" {
			out.Previous, err = a.image(ctx, lines[3])
			if err != nil {
				out.Message = "上一版本镜像不可用"
			}
		}
	} else if !os.IsNotExist(e) {
		return out, errors.New("无法读取发布记录")
	}
	floor := out.Current.Schema
	if b, e = os.ReadFile(filepath.Join(a.cfg.Root, ".deployment", "schema-floor")); e == nil {
		n, e := strconv.Atoi(strings.TrimSpace(string(b)))
		if e != nil || n < 1 {
			return out, errors.New("结构保护记录无效")
		}
		if n > floor {
			floor = n
		}
	} else if !os.IsNotExist(e) {
		return out, errors.New("无法读取结构保护记录")
	}
	out.RollbackAllowed = out.Previous != nil && out.Previous.Schema >= floor && !out.Pending && !out.Locked
	out.UpdateAvailable = out.Release != nil && newer(out.Release.Version, out.Current.Version) && out.Release.Schema >= floor && out.Release.Schema <= 3 && !out.Pending && !out.Locked && out.Current.Health == "healthy"
	return out, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "请求格式无效"})
		return false
	}
	return true
}
func (a *Agent) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		closing := a.closing
		a.mu.Unlock()
		if closing {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		s, e := a.status(ctx)
		if e != nil {
			s.Connected = false
			s.RollbackAllowed = false
			s.UpdateAvailable = false
			s.Message = e.Error()
		}
		writeJSON(w, 200, s)
	})
	m.HandleFunc("POST /v1/check", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		if a.checking || a.closing {
			a.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "正在检查更新"})
			return
		}
		a.checking = true
		a.mu.Unlock()
		defer func() { a.mu.Lock(); a.checking = false; a.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(r.Context(), 18*time.Second)
		defer cancel()
		release, e := a.check(ctx)
		if e != nil {
			writeJSON(w, 502, map[string]string{"error": e.Error()})
			return
		}
		a.mu.Lock()
		a.release = release
		a.mu.Unlock()
		writeJSON(w, 200, release)
	})
	m.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var in p.Request
		if !decode(w, r, &in) {
			return
		}
		j, code, e := a.enqueue(r.Context(), in)
		if e != nil {
			writeJSON(w, code, map[string]string{"error": e.Error()})
			return
		}
		writeJSON(w, 202, j)
	})
	return m
}

func (a *Agent) enqueue(ctx context.Context, in p.Request) (*p.Job, int, error) {
	if !keyPattern.MatchString(in.IdempotencyKey) || in.Actor == "" || len(in.Actor) > 100 || !imagePattern.MatchString(in.ExpectedImage) {
		return nil, 400, errors.New("任务参数无效")
	}
	if in.Action != "update" && in.Action != "rollback" && in.Action != "recover" {
		return nil, 400, errors.New("操作不受支持")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, j := range a.jobs {
		if j.Request.IdempotencyKey == in.IdempotencyKey && j.Request.Actor == in.Actor {
			if j.Request != in {
				return nil, 409, errors.New("重复请求参数不一致")
			}
			return cloneJob(j), 202, nil
		}
	}
	if a.closing || a.active {
		return nil, 409, errors.New("已有部署任务正在执行")
	}
	// Do not call status while holding the mutex; serialize admission separately
	// with active until preflight either succeeds or fails.
	a.active = true
	a.mu.Unlock()
	s, e := a.status(ctx)
	a.mu.Lock()
	if a.closing {
		a.active = false
		return nil, 503, errors.New("部署代理正在停止")
	}
	if e != nil {
		a.active = false
		return nil, 409, e
	}
	// Locked includes our admission marker; check the actual shell lock here.
	if _, e = os.Stat(filepath.Join(a.cfg.Root, ".deployment", "operation.lock")); !os.IsNotExist(e) {
		a.active = false
		return nil, 409, errors.New("发布锁存在，请等待操作结束；中断的锁须由服务器管理员核实后处理")
	}
	if s.Current == nil || s.Current.ID != in.ExpectedImage {
		a.active = false
		return nil, 409, errors.New("当前版本已变化，请重新检查")
	}
	var release *p.Release
	switch in.Action {
	case "update":
		if s.Pending || s.Current.Health != "healthy" || a.release == nil || a.release.Version != in.Version || a.release.SHA256 != in.SHA256 || a.release.Schema > 3 || !newer(a.release.Version, s.Current.Version) {
			a.active = false
			return nil, 409, errors.New("更新信息已失效、代理不兼容或当前服务不健康，请重新检查")
		}
		if a.release.Schema < s.Current.Schema || a.release.Schema > s.Current.Schema && !in.AllowSchemaUpgrade {
			a.active = false
			return nil, 409, errors.New("结构版本不兼容或尚未确认不可逆迁移")
		}
		v := *a.release
		release = &v
	case "rollback":
		if s.Pending || s.Previous == nil || s.Previous.ID != in.TargetImage || s.Previous.Schema < s.Current.Schema {
			a.active = false
			return nil, 409, errors.New("没有兼容的可回退版本")
		}
	case "recover":
		if !s.Pending {
			a.active = false
			return nil, 409, errors.New("没有需要恢复的中断部署")
		}
	}
	b := make([]byte, 16)
	if _, e = rand.Read(b); e != nil {
		a.active = false
		return nil, 500, e
	}
	stamp := time.Now().Unix()
	j := &p.Job{ID: hex.EncodeToString(b), Request: in, Release: release, Status: "queued", Stage: "queued", Message: "任务已接收", CreatedAt: stamp, UpdatedAt: stamp}
	if e = a.save(j); e != nil {
		a.active = false
		return nil, 500, errors.New("任务记录无法持久化，操作未执行")
	}
	if e = atomicFile(filepath.Join(a.cfg.Root, ".deployment", "agent", "last"), []byte(j.ID+"\n")); e != nil {
		a.active = false
		return nil, 500, errors.New("任务索引无法持久化，操作未执行")
	}
	a.jobs[j.ID] = j
	a.last = j
	a.wg.Add(1)
	go a.execute(j)
	return cloneJob(j), 202, nil
}
