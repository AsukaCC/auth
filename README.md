# Auth Center

React + TypeScript + Go 身份与访问管理系统。使用 SQLite 持久化，首次启动时根据 `.env` / 环境变量创建管理员，网页仅提供登录，没有公开的管理员初始化入口。

后续开发请先阅读 [AGENTS.md](AGENTS.md)。使用 Docker 部署可直接跳到下方的 [Docker 部署](#docker-部署)。

## 本地运行

需要 Go 1.26+、Node.js 22+。

```sh
# 在项目根目录准备配置，然后填写管理员邮箱和密码
cp .env.example .env

# 终端一，项目根目录
 go run ./cmd/server

# 终端二
 cd web
 npm ci
 npm run dev
```

在 `.env` 中填写 `AUTH_ADMIN_EMAIL`、`AUTH_ADMIN_PASSWORD`，可选填写 `AUTH_ADMIN_NAME`，再启动服务。访问 http://127.0.0.1:5173 ，使用配置的管理员账户登录。开发服务器将 `/api` 和 `/v1` 转发到 Go 服务。使用其他地址时，设置 `AUTH_PUBLIC_URL` 为浏览器实际访问的完整地址。

## 管理员初始化

- `AUTH_ADMIN_EMAIL`：首次启动必填，必须是有效邮箱。
- `AUTH_ADMIN_PASSWORD`：首次启动必填，长度为 10–72 字节；没有默认密码。
- `AUTH_ADMIN_NAME`：可选，默认“管理员”，最多 80 字节。

服务在开始监听 HTTP 之前检查用户表：如果没有用户，则验证配置并创建管理员；配置缺失或无效会直接退出，不会提供网页初始化入口。密码只以 bcrypt 摘要写入数据库。原来的 `POST /api/setup` 已移除，返回 404；`GET /api/status` 返回 `mail_enabled`、`version`（程序版本）和 `schema_version`（支持的数据库结构版本），不再包含 `setup_required`。

用户表已有数据时跳过初始化，兼容旧版已通过网页创建的账户。重启、升级或修改 `.env` 不会覆盖已有邮箱、姓名、角色、停用状态、密码或 2FA，也不会根据新邮箱增建管理员。修改密码请通过账户安全或找回流程操作；初始化配置不是重置密码接口。

本地 Go 程序自动读取当前工作目录的 `.env`，已有进程环境变量优先。Docker Compose 从 `.env` 读取上述配置并注入容器，文件不会打包进镜像。密码含 `$`、`#`、空格等字符时，使用 dotenv 的单引号格式，例如 `AUTH_ADMIN_PASSWORD='这里填写你设置的密码'`，不要使用示例文字作为实际密码。不要输出完整 `docker compose config` 或容器环境到日志，以免泄露初始化密码。

## 功能

- 管理员：用户角色与停用、第三方 SMTP 邮件配置和测试、找回申诉、全局审计。
- 开发者：创建自己的应用，为应用签发、查看与撤销 API Key。管理员可管理所有应用。
- 普通用户：密码修改、TOTP 验证器绑定与关闭、一次性备用码、查看并撤销自己的应用登录会话。
- 账户恢复：邮件启用时发送 30 分钟有效的一次性找回链接；未启用时只允许管理员核实申诉并提供找回链接。邮件不可用时也能申诉。
- 邮件支持 SMTP STARTTLS（通常 587）和隐式 TLS（通常 465），可使用邮箱服务商的专用授权码。
- 第三方登录同样执行 2FA 验证，第三方会话不能访问控制台管理 API。
- 在线状态以最近 120 秒内收到心跳为准，不代表设备实时网络连接；应用需每 30 秒调用心跳接口。

密码用 bcrypt 保存；API Key、会话 Token、恢复 Token 和备用码只保存 SHA-256 摘要。SMTP 密码和 TOTP 密钥用 AES-256-GCM 加密。Cookie 为 HttpOnly、SameSite=Strict；写接口检查 Origin；认证和安全验证请求有 IP 频率限制；6 位 TOTP 验证码防重放。

## 第三方接入

先由管理员将账户授予“开发者”角色，再创建应用与 API Key。Key 的完整值仅在创建时显示，必须存放在应用服务端。

| 端点 | 权限 | 请求内容 |
| --- | --- | --- |
| `POST /v1/auth/register` | `auth:register` | `email`、`name`、`password` |
| `POST /v1/auth/login` | `auth:login` | `email`、`password`、可选 `code` |
| `GET /v1/session` | `session:read` | Bearer Token |
| `POST /v1/session/heartbeat` | `session:read` | Bearer Token |
| `POST /v1/auth/logout` | 任意有效的本应用 Key | Bearer Token |

所有接口都需要 `X-API-Key`。会话接口还需要 `Authorization: Bearer <access_token>`，Key 与 Token 必须属于同一应用。注册只创建全局普通用户，不自动登录；账户可在不同应用登录，管理员角色不会通过注册 API 授予。Key 权限约束应用调用的接口，并不限制已有用户可登录哪些应用；本版本不提供应用用户白名单、OAuth/OIDC、联合登录或 OAuth 同意页面。

```sh
# 在应用服务端调用
curl http://127.0.0.1:8080/v1/auth/register \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: YOUR_API_KEY' \
  -d '{"email":"user@example.com","name":"示例用户","password":"a-long-password"}'

curl http://127.0.0.1:8080/v1/auth/login \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: YOUR_API_KEY' \
  -d '{"email":"user@example.com","password":"a-long-password","code":"123456"}'

curl http://127.0.0.1:8080/v1/session \
  -H 'X-API-Key: YOUR_API_KEY' \
  -H 'Authorization: Bearer ACCESS_TOKEN'
```

未启用 2FA 时省略 `code`。启用时可填写 TOTP 验证码或备用码。缺少验证码会返回 `401` 和 `requires_2fa: true`。TOTP 验证码接受前后 30 秒时间偏差，同一码只能使用一次；绑定时使用的码也会被消耗。

登录成功响应：

```json
{
  "access_token": "...",
  "token_type": "Bearer",
  "expires_in": 86400,
  "user": {"id": "...", "email": "user@example.com", "name": "示例用户", "role": "user", "tfa": true, "disabled": false, "created_at": 1793318400}
}
```

第三方 Token 有效期 24 小时，控制台 Cookie 有效期 7 天；无自动续期。注销、用户撤销会话、停用账户或密码找回后立即失效。修改密码和启用 2FA 保留当前控制台会话、撤销其他会话。撤销 Key 立即阻止该 Key 调用，其他本应用的有效 Key 仍可维护该应用的已有会话。开发者被降为普通用户或停用时，其应用 Key 不可用；角色恢复后，未撤销的 Key 可继续使用。

错误统一为 `{"error":"原因"}`：400 参数或恢复凭据无效，401 认证失败，403 权限不足，404 资源不存在，409 状态冲突，429 频率限制。每个真实连接 IP 每分钟最多 30 次认证、安全验证和第三方 POST 请求，不信任客户端 X-Forwarded-For。反向代理后请求共享代理 IP 的限额，大规模部署需在可信网关调整限流机制。

## 账户恢复与申诉

邮件找回对存在与不存在的邮箱返回相同成功提示，避免枚举账户；实际发信失败会写审计日志。密码至少 10 字节、最多 72 字节（bcrypt 上限）。

管理员需先核实申诉人的账户归属，填写处理说明后批准或拒绝。批准会作废先前找回链接，并显示一个新的 30 分钟链接，管理员通过已核实的联系方式交给用户；系统不自动向申诉联系方式发送任何信息。实际重置密码时，撤销账户所有登录会话。

重置密码默认保留两步验证。验证器丢失时可使用备用码，或由管理员在申诉处理中明确勾选“同时重置两步验证”；这会立即清除验证器与备用码并撤销会话。管理员不能批准自己的验证器重置申诉，需由其他管理员处理。

## 构建与部署

```sh
make build
AUTH_PUBLIC_URL=http://127.0.0.1:8080 ./auth-server
```

Go 服务直接托管构建后的 React 文件，访问 http://127.0.0.1:8080 。

环境变量（本地参考 `.env.example`；程序自动加载 `.env`，进程环境变量优先）：

| 变量 | 默认值 | 用途 |
| --- | --- | --- |
| `AUTH_ADDR` | `127.0.0.1:8080` | 监听地址 |
| `AUTH_DATA_DIR` | `./data` | SQLite 和加密主密钥目录 |
| `AUTH_WEB_DIR` | `./web/dist` | 前端构建目录 |
| `AUTH_PUBLIC_URL` | `http://127.0.0.1:5173` | 允许的前端 Origin、找回链接基址 |
| `AUTH_SECURE_COOKIE` | `false` | HTTPS 部署设为 `true` |
| `AUTH_ADMIN_EMAIL` | 无 | 空用户表首次启动时创建的管理员邮箱 |
| `AUTH_ADMIN_PASSWORD` | 无 | 首次管理员密码，10–72 字节；只用于初始化 |
| `AUTH_ADMIN_NAME` | `管理员` | 首次管理员姓名 |

生产部署应在 HTTPS 反向代理后运行，设置实际 `AUTH_PUBLIC_URL` 和 `AUTH_SECURE_COOKIE=true`，启动前配置管理员凭据。备份整个 `AUTH_DATA_DIR`（包含 `auth.db`、SQLite WAL 和 `master.key`），建议停服或使用 SQLite 在线备份机制保证一致性。丢失主密钥将无法解密邮件凭据与 TOTP 密钥。

## Docker 部署

需要 Docker Engine / Docker Desktop 和 Docker Compose v2.20+。镜像会构建 React 和 Go，宿主机无需安装 Node.js、Go 或 SQLite。一个容器同时提供前端与 API。

### 一键启动

在仓库根目录复制 Docker 配置样例，填写管理员邮箱和密码后再启动。如果已经有 `.env`，直接补充配置，不要覆盖原文件：

```sh
cp .env.docker.example .env
# 编辑 .env，填写 AUTH_ADMIN_EMAIL 和 AUTH_ADMIN_PASSWORD
docker compose up -d --build --wait
docker compose ps
```

配置完成后也可使用 `make docker-up`。浏览器访问 http://127.0.0.1:8080 ，使用 `.env` 中配置的管理员账户登录。默认仅绑定本机回环地址，并在 `auth-center_auth-data` 命名卷中保存数据库和主密钥；镜像及容器重建不会清空数据。该卷独立于直接运行 Go 时使用的本地 `./data` 目录。首次未填写管理员配置时容器会退出，`--wait` 不能成功；通过 `docker compose logs auth` 查看缺少哪个配置项。

容器以 UID/GID `10001:10001` 运行，根文件系统只读，只有 `/data` 数据卷和 `/tmp` 可写。`/healthz` 检查数据库连接，正常返回 HTTP 204，Docker 状态显示 `healthy`。健康状态不会检查第三方 SMTP 的送达情况。

### 修改端口与访问域名

编辑现有 `.env` 中的端口或域名后，重新启动：

```sh
# 编辑 .env
docker compose up -d --wait
```

Compose 会自动读取 `.env`；请使用 `.env.docker.example`，不要复制面向本机 Go 开发的 `.env.example`。

| Compose 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `AUTH_BIND_ADDRESS` | `127.0.0.1` | 宿主机端口绑定地址；需要其他机器直接访问时改为 `0.0.0.0` |
| `AUTH_PORT` | `8080` | 宿主机端口；修改时同步更新 `AUTH_PUBLIC_URL` |
| `AUTH_PUBLIC_URL` | `http://127.0.0.1:8080` | 用户实际访问地址，也是邮件找回链接的基址 |
| `AUTH_SECURE_COOKIE` | `false` | 用户通过 HTTPS 访问时设为 `true` |
| `AUTH_ADMIN_EMAIL` | 无 | 空用户表首次启动必填 |
| `AUTH_ADMIN_PASSWORD` | 无 | 空用户表首次启动必填，10–72 字节 |
| `AUTH_ADMIN_NAME` | `管理员` | 首次创建的管理员姓名 |

例如在宿主机使用 Nginx / Caddy 提供 `https://auth.example.com`，将请求反向代理到 `http://127.0.0.1:8080`，并设置：

```dotenv
AUTH_BIND_ADDRESS=127.0.0.1
AUTH_PORT=8080
AUTH_PUBLIC_URL=https://auth.example.com
AUTH_SECURE_COOKIE=true
```

上面的域名设置与管理员初始化配置放在同一个 `.env` 中，管理员会在服务启动时自动创建。HTTP 调试时保留 `AUTH_SECURE_COOKIE=false`，否则浏览器不会正常回传登录 Cookie。

### 日常管理与升级

下面是源码部署命令。需要安装包版本记录、自动备份和回退时，使用后面的“版本发布、更新与回退”，不要混用手工 `compose up --build` 与发布管理命令。

```sh
# 查看状态与最近日志
docker compose ps
docker compose logs -f --tail=100 auth

# 更新源代码后重新构建并替换容器，保留命名数据卷
docker compose up -d --build --wait

# 停止并移除容器，保留数据卷
docker compose down

# 再次启动
docker compose up -d --wait
```

不要给停止命令添加 `-v`，该参数会删除数据库和主密钥所在的数据卷。当前 SQLite 部署保持一个服务实例，不要对 `auth` 使用多副本扩容。

### 版本发布、更新与回退

发布者在源码目录执行（仅本地生成安装包，不会自动上传或发布到 GitHub）：

```sh
# 默认版本来自 VERSION，默认目标是 Linux x86_64
make release
# 或指定新的版本号、目标服务器架构
AUTH_RELEASE_PLATFORM=linux/arm64 ./scripts/release.sh 1.0.1
```

输出 `dist/auth-center-<版本>-<架构>.tar.gz`，内含离线 Docker 镜像、校验值、Compose 和 `authctl.sh`。相同输出文件不允许覆盖。正式发布应先执行项目测试，同一版本号只能对应一份构建。安装包仅从受信任的渠道获取；SHA-256 校验用于检测损坏，并不代替发行者签名。版本号编译到程序中，控制台左下角和 `/api/status` 可查看；直接 `go run` 或普通 Docker 开发构建显示 `dev`。

部署主机只需 Docker、Compose 和常见 Unix 工具（sh、tar、sha256sum 或 shasum），不需要 Go、Node.js。首次安装示例：

```sh
# 将下载的发行包放在 /tmp 等目录，所有版本都解压到同一个 auth-center 目录
tar -xzf /tmp/auth-center-1.0.0-amd64.tar.gz
cd auth-center
cp .env.docker.example .env
# 编辑 .env，填写管理员和访问地址
./authctl.sh install
./authctl.sh status
```

新版本发布后，在 **auth-center 的父目录** 解压新包，覆盖工具和版本指针；安装包不会包含或覆盖 `.env`、`.deployment`、备份和数据卷：

```sh
tar -xzf /tmp/auth-center-1.0.1-amd64.tar.gz
cd auth-center
./authctl.sh update
# 需要时切回上一个成功部署的镜像
./authctl.sh rollback
```

- 更新先校验镜像和包摘要、记录操作状态，停服备份整个数据卷，再启动新版本并等待健康检查。新版本失败时尝试自动切回原镜像；失败操作返回非零退出码。
- **安装目录的 `packages/` 只保留当前与上一可回退版本**。成功更新或回退后清理更早的托管镜像包；重复 `update` 相同版本不会重新安装。不要把用户文件放入 `packages/`。源码端 `dist/` 的发行存档、外部下载目录、Docker 缓存及数据备份不会被自动删除。
- `backups/` 中的数据库、WAL 和主密钥备份采用私有权限单独保留，请定期转存到安全位置并按自己的保留周期清理。它们不属于旧安装包。
- 回退切换镜像并保留当前数据库，包括更新之后新建的账户与会话，不恢复旧备份、不丢弃新数据。目前仅支持结构版本 `1` 内的更新与回退；跨结构更新需要专门的迁移方案。程序会给旧数据库补记版本 `1`，遇到更高数据库版本会拒绝启动。
- `.deployment/` 保存当前/上一镜像的不可变 ID、各版本 Compose 快照和操作记录；请保留。进程中断后执行 `./authctl.sh recover`。若操作锁仍存在，先确认没有发布命令在运行，再执行 `rmdir .deployment/operation.lock` 后重试；不要删除整个 `.deployment`。
- 数据目录有进程级文件锁，退出后由系统释放；不要手工删除 `.instance.lock`。同一数据目录禁止多实例并行访问。
- `install` 检测已有实例/数据卷后拒绝重复安装；已有源码 Compose 部署可以在**原部署目录**解压发布包内容后执行 `./authctl.sh adopt`，纳入版本管理。需先启动原部署，默认项目名 `auth-center`，标准数据卷 `auth-center_auth-data`，原镜像保留为 `legacy` 回退点。自定义 Compose 项目首次接管时通过 `AUTH_PROJECT_NAME` 指定，之后固定记录，不要切换安装目录。

所有发布操作在本机命令行执行，需要服务器 Docker 权限；网页仅显示版本，不提供挂载 Docker Socket 的高权限更新接口。当前为短暂停服更新，并不提供无停机升级。

### 备份与恢复

完整备份需要同时包含数据库和 `master.key`。停服后导出数据卷，避免复制处于写入中的 SQLite 数据库：

```sh
umask 077
mkdir -p backups
docker compose stop auth
docker compose run --rm --no-deps -T --entrypoint tar auth \
  -czf - -C /data . > backups/auth-data.tar.gz
docker compose up -d --wait
```

备份目录已排除出 Git 和 Docker 构建上下文。恢复应在**空数据卷的新部署**中进行，不能覆盖正在运行的数据库；保留旧数据卷以便回退：

```sh
docker compose build
docker compose run --rm --no-deps -T --entrypoint tar auth \
  -xzf - -C /data < backups/auth-data.tar.gz
docker compose up -d --wait
```

如果要将原来的本机 `./data` 迁移到 Docker，先停止本机 Go 服务，然后归档整个目录，再按上面的空卷恢复步骤导入。不要只复制 `auth.db` 而遗漏主密钥或 WAL：

```sh
umask 077
mkdir -p backups
tar -czf backups/auth-data.tar.gz -C data .
```

### 不使用 Compose

先将 `AUTH_ADMIN_EMAIL`、`AUTH_ADMIN_PASSWORD`、`AUTH_ADMIN_NAME` 导出为当前 shell 的环境变量，再运行以下命令。下面的 `-e` 只传变量名，避免把密码写入命令参数；使用 `.env` 文件部署时推荐前面的 Compose 方式。

```sh
docker build -t auth-center:local .
docker run -d --name auth-center --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e AUTH_PUBLIC_URL=http://127.0.0.1:8080 \
  -e AUTH_ADMIN_EMAIL -e AUTH_ADMIN_PASSWORD -e AUTH_ADMIN_NAME \
  -v auth-center-data:/data auth-center:local
```

这个命令使用的 `auth-center-data` 卷与 Compose 的默认卷不同。两种运行方式选择其一，迁移时通过备份恢复数据。

## 验证

```sh
go test ./...
go vet ./...
cd web && npm run build
```

测试覆盖环境配置初始化、无效配置拒绝启动、重启不覆盖已有账户、公开初始化接口移除、dotenv 加载与环境变量优先级、角色与应用归属、API 权限范围、第三方/控制台会话隔离、密钥撤销、TOTP 防重放、备用码单次使用、申诉找回、恢复链接过期与重用、邮件凭据加密及持久化、Origin 检查、限流、停用账户和会话过期。SMTP 真实送达需在管理员邮件页配置实际服务商后测试。
