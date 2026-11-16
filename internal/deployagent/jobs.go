package deployagent

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	p "authcenter/internal/deployprotocol"
)

func (a *Agent) execute(j *p.Job) {
	defer a.wg.Done()
	defer func() { a.mu.Lock(); a.active = false; a.mu.Unlock() }()
	if a.stage(j, "running", "preflight", "正在核对当前部署") != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	s, err := a.status(ctx)
	if err != nil || s.Current == nil || s.Current.ID != j.Request.ExpectedImage {
		a.stage(j, "failed", "preflight", "当前部署已变化，未执行更新")
		return
	}
	action := j.Request.Action
	var target string
	if action == "update" {
		if a.stage(j, "running", "download", "正在下载并校验发布包；服务继续运行") != nil {
			return
		}
		target, err = a.prepare(ctx, j)
		if err != nil {
			a.stage(j, "failed", "download", err.Error())
			return
		}
		if !s.Managed {
			action = "adopt"
		}
	}
	if a.stage(j, "running", "deploy", "正在停服备份、切换镜像并检查健康；网页可能暂时断开") != nil {
		return
	}
	env := []string{"AUTH_PROJECT_NAME=" + a.project, "AUTH_EXPECT_CURRENT_ID=" + j.Request.ExpectedImage, "AUTH_EXPECT_PREVIOUS_ID=" + j.Request.TargetImage, "AUTH_ALLOW_SCHEMA_UPGRADE=false"}
	if j.Request.AllowSchemaUpgrade {
		env[len(env)-1] = "AUTH_ALLOW_SCHEMA_UPGRADE=true"
	}
	env = append(env, "AUTH_DEPLOY_AGENT_REQUEST=true")
	if j.Release != nil {
		env = append(env, "AUTH_TARGET_VERSION="+j.Release.Version, "AUTH_EXPECT_TARGET_ID="+target)
	}
	// The runner receives only a fixed script and an enumerated action. Neither
	// package scripts nor browser-controlled strings are executable commands.
	_, err = a.run(ctx, a.cfg.Root, []string{"sh", filepath.Join(a.cfg.Root, "authctl.sh"), action}, env)
	if a.stage(j, "running", "health", "正在确认部署结果") != nil {
		return
	}
	final, checkErr := a.status(ctx)
	if err == nil && checkErr == nil && final.Current != nil && final.Current.Health == "healthy" && !final.Pending {
		valid := action == "recover" || action == "rollback" && final.Current.ID == j.Request.TargetImage || (action == "update" || action == "adopt") && final.Current.ID == target
		if valid {
			a.stage(j, "succeeded", "done", "操作完成，服务健康，数据卷已保留")
			return
		}
	}
	message := "操作失败，请检查服务器部署状态"
	if final.Pending {
		message = "操作未完成，已保留数据、备份与恢复记录；请执行恢复"
	} else if final.Current != nil && final.Current.ID == j.Request.ExpectedImage && final.Current.Health == "healthy" {
		message = "更新失败，已恢复原镜像，原服务健康"
	}
	if ctx.Err() != nil {
		message = "操作超时，请核对发布锁和恢复记录后处理"
	}
	a.stage(j, "failed", "failed", message)
}

// Check the tools we depend on once at startup, without displaying command output
// that could include Compose environment values.
func (a *Agent) CheckTools(ctx context.Context) error {
	for _, args := range [][]string{{"info", "--format", "{{.ServerVersion}}"}, {"compose", "version", "--short"}} {
		if _, err := a.docker(ctx, args...); err != nil {
			return errors.New("部署代理无法访问 Docker 或 Compose")
		}
	}
	return nil
}
