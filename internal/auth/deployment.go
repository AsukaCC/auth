package auth

import (
	"authcenter/internal/deployprotocol"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

func (a *App) deployment(w http.ResponseWriter, r *http.Request, s *session) {
	if a.cfg.DeploySocket == "" {
		jsonOut(w, 200, deployprotocol.Status{Message: "部署代理尚未启用，请先完成服务器的一次性配置"})
		return
	}
	a.agentRequest(w, r, "GET", "/v1/status", nil)
}

func (a *App) deploymentAction(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Action             string `json:"action"`
		Version            string `json:"version"`
		SHA256             string `json:"sha256"`
		ExpectedImage      string `json:"expected_image"`
		TargetImage        string `json:"target_image"`
		IdempotencyKey     string `json:"idempotency_key"`
		AllowSchemaUpgrade bool   `json:"allow_schema_upgrade"`
		Password           string `json:"password"`
		Code               string `json:"code"`
	}
	if !decode(w, r, &in) {
		return
	}
	if a.cfg.DeploySocket == "" {
		fail(w, 503, "部署代理尚未启用")
		return
	}
	if in.Action == "check" {
		a.agentRequest(w, r, "POST", "/v1/check", map[string]string{})
		return
	}
	if in.Action != "update" && in.Action != "rollback" && in.Action != "recover" {
		fail(w, 400, "操作不受支持")
		return
	}
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 || len(in.Version) > 80 || len(in.ExpectedImage) > 80 || len(in.TargetImage) > 80 || len(in.SHA256) > 64 {
		fail(w, 400, "任务参数无效")
		return
	}
	if !a.verifyPassword(s.UserID, in.Password) {
		fail(w, 403, "管理员密码不正确")
		return
	}
	if s.User.TFA && !a.verifyCode(s.UserID, in.Code) {
		fail(w, 403, "请输入尚未使用的有效两步验证码")
		return
	}
	// Audit the authorized request before the independent agent can stop Auth.
	if _, err := a.db.Exec("INSERT INTO audit VALUES(?,?,?,?,?,?)", id(), s.UserID, "deployment.request", in.Action+":"+in.IdempotencyKey, ip(r), now()); err != nil {
		fail(w, 500, "无法记录更新审计，操作未执行")
		return
	}
	a.agentRequest(w, r, "POST", "/v1/jobs", deployprotocol.Request{Action: in.Action, Version: in.Version, SHA256: in.SHA256, ExpectedImage: in.ExpectedImage, TargetImage: in.TargetImage, IdempotencyKey: in.IdempotencyKey, AllowSchemaUpgrade: in.AllowSchemaUpgrade, Actor: s.UserID})
}

func (a *App) agentRequest(w http.ResponseWriter, r *http.Request, method, path string, data any) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", a.cfg.DeploySocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 22 * time.Second}
	var body io.Reader
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			fail(w, 500, "任务编码失败")
			return
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, "http://deploy-agent"+path, body)
	if err != nil {
		fail(w, 500, "代理请求失败")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		if method == "GET" {
			jsonOut(w, 200, deployprotocol.Status{Enabled: true, Message: "无法连接部署代理；若正在更新，请等待服务恢复后重试"})
		} else {
			fail(w, 503, "部署代理暂不可用，操作结果未知时请先刷新状态，勿重复创建任务")
		}
		return
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 128*1024+1))
	if err != nil || len(b) > 128*1024 || !json.Valid(b) {
		fail(w, 502, "部署代理响应无效")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(b)
}
