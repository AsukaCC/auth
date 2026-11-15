# 管理员网页更新

管理员进入「系统更新」，检查 GitHub 正式发布，验证管理员密码后提交任务。已启用两步验证时还需提供新的 TOTP 或未使用的备用码。更新由独立服务器代理执行，Auth 容器重建不会中断代理。

## 首次启用（服务器执行一次）

需要 Linux amd64/arm64、Docker Engine 和 Compose v2。使用标准单实例、命名数据卷部署。

1. 将包含此功能的完整发行包解压到**原安装目录**，保留 `.env`、`.deployment/` 和数据卷。先按 README 的 `update`（已有发布管理）或 `adopt`（原源码部署）升级一次到包含网页更新的版本。全新安装执行 `install`。跨结构升级仍需显式设置 `AUTH_ALLOW_SCHEMA_UPGRADE=true`。
2. 在原 `.env` 中加入以下配置。项目名必须与 `.deployment/project` 一致（默认 `auth-center`），不要更换原项目或数据卷名：

```dotenv
AUTH_DEPLOY_SOCKET=/run/auth-deploy/agent.sock
AUTH_PROJECT_NAME=auth-center
AUTH_UPDATE_REPOSITORY=AsukaCC/auth
```

3. 加载附带的代理镜像，在原安装目录启动独立的代理项目：

```sh
docker load -i deploy-agent/image.tar
AUTH_DEPLOY_AGENT_IMAGE=$(cat deploy-agent/image.id) \
  docker compose -f compose.deploy.yaml up -d --no-build --pull never
docker compose -f compose.deploy.yaml ps
```

4. 让 Auth 重新读取 `.env`，使用记录的不可变镜像重建，保留数据卷。在原安装目录执行：

```sh
project=$(cat .deployment/project)
version=$(sed -n '1p' .deployment/state)
image=$(sed -n '2p' .deployment/state)
AUTH_IMAGE="$image" docker compose --project-name "$project" --env-file .env \
  --project-directory "$PWD" -f ".deployment/versions/$version/compose.yaml" \
  up -d --no-build --pull never --wait auth
```

代理拥有安装目录的读写权限和 Docker 控制权，以 root 运行；Auth 继续以 `10001:10001` 运行，只读挂载专用 Unix Socket 卷，无 Docker 权限。代理无端口映射；Socket 为 UID 10001 所有、权限 0600。不要将 Socket 暴露到网络或共享给其他应用。代理固定安装目录和 GitHub 仓库，不接受网页传入命令、URL、路径或 Compose 配置。

源码部署可先执行 `docker build -f Dockerfile.agent -t auth-center-deploy-agent:local .`，再使用相同 Compose 文件启动代理。生产环境建议使用发行包的代理镜像。

私有仓库：将只读 Contents 权限的 GitHub Token 放在服务器 `.deployment/github-token`，权限设为 600。在 `.env` 设置 `AUTH_GITHUB_TOKEN_FILE=/deployment/.deployment/github-token` 后重建代理。Token 不传给 Auth、不通过网页输入，也不进入任务记录。公共仓库无需 Token。

## 后续发布与网页操作

- 修改 `VERSION` 为更高的正式版本（例如 `1.2.1`）并推送到 `main`，GitHub Actions 在测试通过后构建 Linux amd64/arm64 安装包和更新清单，创建 `v1.2.1` 正式 Release。也支持推送与 VERSION 一致的 `v*` 标签或手动运行 Release 工作流。仓库必须启用 Actions，发布工作流需有 Contents 写入权限。
- **仅推送源码、没有递增 VERSION，不会产生新正式版本。**构建期间无法更新；等待 Release 完整发布后检查新版本。相同版本不能覆盖。
- 网页点击「检查新版本」→「准备更新」→输入密码和两步验证码→确认。代理核对已检查版本的摘要和当前镜像 ID，拒绝过期请求。跨结构迁移需额外勾选确认。
- 下载后校验完整安装包 SHA-256、大小、内部镜像摘要、镜像版本和结构标签，只导入镜像与元数据，不执行下载包中的脚本，也不替换本机 Compose。更新依次执行停服、备份整个数据卷、切换固定镜像、等待 Docker 健康检查并再次核对运行镜像。期间网页短暂断开会自动重连，会话和主密钥保留。
- 任务持久化到 `.deployment/agent/`，页面每 4 秒读取状态。相同任务键返回原任务，不重复执行。并发任务、命令行发布锁、同版本不同内容会被拒绝。管理员请求写入认证审计，结果保留在代理状态目录；不保存密码或验证码。
- 同结构健康检查失败会自动恢复原不可变镜像，任务仍标记失败。页面可手动「回退上一版本」，保留当前数据库和更新后新增数据。跨结构迁移后禁止回退旧结构，失败仅向前恢复兼容新镜像。

SHA-256 用于检测内容变化；发行信任基于固定配置的 GitHub 仓库与 HTTPS，并非独立签名验证。代理通过 `/releases/latest` 获取正式版本。

## 中断与维护

代理重启后将未完成任务标为「已中断」，不自动重跑。若有 `.deployment/pending` 且 Auth 尚可访问，可使用网页「恢复中断部署」。如果 Auth 已停止而无法打开页面，必须在服务器执行 `./authctl.sh recover`。若 `.deployment/operation.lock` 遗留，先确认没有部署进程运行，再按 README 清理该空锁目录；网页不会强行删除锁。

网页更新不会替换部署工具和代理本身。后续如升级部署协议、Compose、迁移兼容范围或代理代码，应由服务器管理员下载完整新发行包，审查并升级本机工具及代理，再更新应用。当前工具支持结构 1/2，拒绝不支持的更高结构。

代理与 Auth 分属两个 Compose 项目。维护时用 `docker compose -f compose.deploy.yaml stop` 停止代理，会等待在途任务完成；不要在发布期间强制杀死代理。保留安装目录、`.deployment/`、`backups/` 和数据卷。

Linux 上代理创建的部署状态、任务和备份属于 root，采用 0600/0700 私有权限。启用代理后的宿主机命令行维护请使用 `sudo ./authctl.sh status`、`sudo ./authctl.sh recover` 等；不要放宽这些文件权限。隔离发布测试同样以 `sudo python3 scripts/test_release.py <发行包>` 运行，确保能读取任务状态并清理本次测试目录。

## 管理 API

两条接口仅接受有效的**管理员控制台 Cookie 会话**；开发者、普通用户和第三方 Bearer 会话均不可访问。沿用 Origin 检查与 POST 限流。

| 请求 | 用途 |
| --- | --- |
| `GET /api/admin/deployment` | 读取连接、当前/上一镜像、结构、健康、最近任务及发布信息；未启用时返回 `enabled:false` |
| `POST /api/admin/deployment` | `action:check` 检查固定发布源；`update/rollback/recover` 创建任务，成功返回 202 |

创建任务字段：`action`、`expected_image`（当前 sha256 镜像 ID）、`idempotency_key`（16–128 位字母/数字/下划线/短横线）、`password`、`code`（启用 2FA 时必填）。`update` 还需检查结果的 `version`、`sha256`，跨结构时 `allow_schema_upgrade:true`；`rollback` 还需 `target_image`（上一镜像 ID）。密码错误或验证码重放返回 403，过期状态/并发/不兼容返回 409，代理不可用返回 503。结果不明确时先查询状态，再使用原任务键及新的验证码重试。
