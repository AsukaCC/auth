# 项目开发指南

本文件适用于整个仓库。Auth Center 是 React + TypeScript + Go 的身份与访问管理系统，使用 SQLite 持久化；界面和用户提示默认使用简体中文。

## 项目结构

- `cmd/server/main.go`：服务入口、环境变量和 HTTP 服务生命周期。
- `internal/auth/store.go`：SQLite 表结构、持久化、主密钥和加密工具。
- `internal/auth/http.go`：路由、认证中间件、Origin 检查和限流。
- `internal/auth/account.go`：控制台登录、密码、会话、TOTP 和审计。
- `internal/auth/apps.go`：应用归属、API Key、第三方接口和用户角色。
- `internal/auth/recovery.go`：SMTP、邮件找回和管理员申诉处理。
- `internal/auth/auth_test.go`：基于临时数据库和 httptest 的行为测试。
- `web/src/main.tsx`：React 页面、表单、弹窗和 API 调用。
- `web/src/style.css`：界面样式和响应式布局。
- `Dockerfile`、`compose.yaml`、`.env.docker.example`：容器构建与部署。
- `README.md`：启动、接口约定、部署和数据备份说明。

## 常用命令

```sh
# 开发：分别在两个终端运行
go run ./cmd/server
cd web && npm ci && npm run dev

# 后端检查，在仓库根目录运行
go test ./...
go vet ./...

# 修改认证、并发或共享状态后执行
go test -race ./...

# 前端类型检查与生产构建
cd web && npm run build

# 构建本机二进制和前端
make build

# Docker 配置检查与部署
docker compose config --quiet
docker compose up -d --build --wait
docker compose ps
```

开发地址为 `http://127.0.0.1:5173`，Go 默认监听 `127.0.0.1:8080`。Vite 代理 `/api` 和 `/v1`；生产环境由 Go 直接托管 `web/dist`。`/healthz` 用于容器健康检查。

## 修改约定

- 先阅读相关模块，沿用现有 Go 标准库 HTTP、database/sql、React 和 lucide-react 的用法。
- 修改保持聚焦；不要因小功能重构全部路由、引入新的前端框架或替换数据库。
- 手工编辑使用 `apply_patch`；Go 修改后运行 `gofmt`。不要改生成的 `web/dist`、`node_modules` 或二进制。
- 前端保持现有中文界面、响应式布局、可访问的表单标签、加载状态和错误反馈。
- 新增或变更 API 时同时更新后端、前端调用和 README 中的权限及请求约定。
- 影响权限、会话、密码恢复或 2FA 的修改需要对应行为测试，覆盖拒绝路径和凭据重放。
- 测试使用 `t.TempDir()` 和独立数据库，不读写用户的 `data/`，不发送真实邮件。
- 检查仓库未提交改动，保留用户修改。没有明确要求时不要提交、推送或重置 Git 状态。

## 必须保持的认证约束

- 角色为 `admin`、`developer`、`user`。权限与应用归属必须在后端校验，不能只隐藏前端按钮。
- 开发者仅管理自己的应用和 Key；管理员可管理全部应用。第三方注册只能创建普通用户。
- 控制台使用 HttpOnly Cookie；第三方会话使用 Bearer Token，不能用于控制台管理接口。
- 第三方接口必须验证 `X-API-Key`、scope、应用归属，以及应用所有者的当前角色和停用状态。
- 密码使用 bcrypt；Token、API Key、找回凭据和备用码仅保存摘要；SMTP 凭据和 TOTP 密钥使用 AES-GCM 加密。
- 不把凭据写入日志、审计详情、Git、镜像或构建上下文。完整 API Key 和备用码只在创建时返回。
- TOTP 接受的时间窗口必须防重放，包括绑定时的验证码；备用码、找回 Token 都只能使用一次。
- 邮件密码找回保留 2FA；只有经过管理员明确批准的验证器恢复流程才可清除 2FA。
- 密码找回完成后撤销所有会话；保留现有停用账户、修改密码、启用 2FA 的会话失效规则。
- 不信任任意 `X-Forwarded-For`，不为了调通开发环境而移除 Origin 校验或放宽为任意来源。

## 数据库与部署

- 当前 SQLite 连接池只有一个连接。持有未关闭的 `Rows` 或事务时，不要再次通过 `a.db` 发起嵌套查询；事务内部使用 `tx`。
- 表结构的 `CREATE TABLE IF NOT EXISTS` 不会升级已有表。修改存量结构时，提供可重复执行的迁移和已有数据的兼容验证。
- `/data` 必须持久化，并同时保存数据库、WAL 和 `master.key`。主密钥不可在镜像构建时生成或在重建容器时替换。
- 同一个数据卷只运行一个 Auth 实例；不要直接给当前 SQLite 部署增加多副本。
- Docker 使用非 root 用户，前端构建和 Go 编译在多阶段构建中完成；宿主机无需安装 Go 或 Node.js。
- Compose 默认只发布到 `127.0.0.1:8080`。公网部署设置实际 `AUTH_PUBLIC_URL`，通过 HTTPS 代理访问时启用 `AUTH_SECURE_COOKIE=true`。
- 程序不会自动加载 `.env`，Docker Compose 会读取仓库根目录 `.env` 做变量替换；Docker 配置样例是 `.env.docker.example`。
- 不自动执行 `docker compose down -v`、删除用户数据卷或覆盖用户数据库。重建与升级必须保留数据卷；只允许清理本次验证创建且确定不含用户数据的临时测试卷。
- 容器相关修改至少验证 Compose 配置、镜像构建、健康检查、静态资源和重建后的数据持久化；没有可用 Docker 时说明未验证项。
