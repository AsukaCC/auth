# Google / GitHub 登录、账户绑定与业务应用接入

支持 Google 和 GitHub OAuth Authorization Code + S256 PKCE。服务商配置由管理员在网页管理，Client Secret 使用现有 `master.key` 的 AES-GCM 加密保存；不会返回给浏览器，不保存服务商 Access Token / Refresh Token。Google 使用服务端 HTTPS UserInfo 的稳定 `sub`，GitHub 使用数字用户 ID，不接受浏览器提交的邮箱、用户名或任意 ID Token 作为身份凭据。

## 管理员配置

1. 部署站点并设置 `AUTH_PUBLIC_URL=https://auth.example.com`、`AUTH_SECURE_COOKIE=true`。域名、协议和端口必须与用户实际访问地址一致；本机开发使用 `http://127.0.0.1:5173`。通过代理访问时不要将内部 Docker 地址登记为回调。
2. 管理员进入「第三方登录」，在对应服务商创建 OAuth 应用，精确复制页面显示的回调地址：
   - Google：`https://auth.example.com/api/auth/oauth/google/callback`
   - GitHub：`https://auth.example.com/api/auth/oauth/github/callback`
3. Google：在 [Google Cloud](https://console.cloud.google.com/apis/credentials) 配置 OAuth 同意屏幕，创建 **Web 应用** OAuth 客户端。授权范围使用 `openid email profile`；应用处于测试模式时，需要把实际使用者加入测试用户。使用授权码重定向，不需要在前端安装 Google SDK。
4. GitHub：在 [Developer settings](https://github.com/settings/developers) 创建 **OAuth App**，Homepage URL 填本站地址，Authorization callback URL 填上述 GitHub 回调。授权范围为 `read:user user:email`，支持通过邮件接口读取已验证的私有主邮箱。
5. 输入 Client ID、Client Secret，勾选启用，验证当前管理员密码；已开启 2FA 时额外输入新的验证码或备用码。保存后登录页显示入口，账户安全页显示可绑定状态。已保存的 Secret 留空保留；变更 Client ID 必须重新填写 Secret。

每个服务商的「允许首次授权的身份注册普通用户账户」**默认关闭**。关闭注册时，已有账户仍可绑定，已绑定用户仍可登录。开放注册时，首次授权的新身份必须提供已验证邮箱，设置姓名和本地密码后才创建 `user` 角色账户。业务应用还必须拥有 `auth:register` 权限才能在该流程创建用户。

关闭服务不会删除已有绑定，但会禁止该服务的登录和新增绑定。每次保存配置都会使进行中的授权和未交换的授权码失效。公开发现接口仅返回启用的服务名称和注册开关；管理配置读取接口要求管理员权限。Client ID 是 OAuth 公开标识，会出现在服务商授权 URL 中；Client Secret 始终只在本站后端与服务商间交换。

## 已注册账户绑定与解绑

- 先用原账户登录，进入「账户安全」→「第三方账户绑定」，验证本地密码和已启用的 2FA，跳转服务商授权，再回到本站确认绑定。
- 同一 Auth Center 账户每个服务商最多绑定一个身份；同一服务商身份只能归属一个 Auth Center 账户。可以绑定与本地登录邮箱不同的第三方邮箱；绑定不会修改本地邮箱、密码、角色或个人资料。
- **同邮箱不会自动合并账户。** 未绑定身份登录时，如果邮箱已经注册，会提示使用原账户登录后绑定；不会把第三方邮箱视作现有账户的本地认证凭据。
- 绑定开始后必须在同一浏览器、同一有效控制台会话内完成。注销、撤销会话、停用账户、修改密码、改变 2FA 或变更身份绑定后，原绑定授权失效。
- 解绑需要再次验证密码和 2FA，并撤销该用户除当前控制台会话之外的全部会话。第三方服务被关闭后仍可解绑。所有账户均保留本地密码，不会出现解绑最后一个服务后无法登录的情况。
- 第三方登录不会绕过本地 2FA。已开启验证器的账户必须提供尚未使用的 TOTP 或备用码才能完成登录。邮件密码找回仍保留 2FA；验证码防重放规则与密码登录相同。

## 控制台 API

请求及响应均为 JSON，除服务商回调和浏览器跳转接口外。写请求沿用 Origin 校验及认证限流。所有敏感控制台接口只接受 HttpOnly 控制台 Cookie，业务应用 Bearer Token 不能代替。

| 接口 | 权限与请求 | 响应 / 行为 |
| --- | --- | --- |
| `GET /api/auth/oauth/providers` | 公开，无凭据 | `{"providers":[{"provider":"google","allow_signup":false}]}`；只列已启用服务 |
| `GET /api/admin/oauth` | 管理员控制台 Cookie | `providers` 中每项含 `provider, enabled, client_id, has_secret, allow_signup, revision, callback_url`，不返回 Secret |
| `PUT /api/admin/oauth/{provider}` | 管理员；`enabled, client_id, client_secret, allow_signup, expected_revision, password, code` | `{"ok":true}`；`expected_revision` 取 GET 返回值，首次为空串；并发配置冲突 409 |
| `POST /api/auth/oauth/{provider}/start` | 登录为公开；绑定要求控制台 Cookie；`intent:"login"` 或 `intent:"bind"`，绑定时还需 `password, code` | `{"authorization_url":"https://..."}`；设置与 state 绑定的临时 HttpOnly Cookie，浏览器跳转该地址 |
| `GET /api/auth/oauth/{provider}/callback` | 服务商重定向；`code, state` 或 `error, state` | 校验并一次性消费 state 和浏览器 Cookie，交换服务商凭据；设置临时票据 Cookie，303 到 `/?oauth=complete`；失败仅返回固定的 `oauth_error` 标识，不回显上游响应或 Token |
| `GET /api/auth/oauth/pending` | 临时 HttpOnly 票据 Cookie；绑定还要求原控制台 Cookie | `provider, intent, email, name, requires_2fa, requires_registration, application`；不返回票据、PKCE verifier 或上游 Token |
| `POST /api/auth/oauth/complete` | 同上；登录提供 `code`（本地 2FA）；注册提供 `name, password`；绑定提交 `{}` | 控制台登录返回 `{"user":...}` 并设置会话 Cookie；绑定返回 `{"bound":true}`；业务应用返回 `{"redirect_url":"..."}` |
| `POST /api/auth/oauth/cancel` | 临时票据 Cookie（若有） | 作废当前票据并清 Cookie，返回 `{"ok":true}` |
| `GET /api/account/identities` | 本人控制台 Cookie | `{"identities":[{"provider":"github","email":"...","created_at":...}]}`，不公开服务商 subject |
| `POST /api/account/identities/{provider}/unlink` | 本人控制台 Cookie；`password, code` | `{"ok":true}`，撤销其他会话 |
| `GET /api/applications/{id}/oauth` | 应用所有者（developer/admin）或管理员 | `{"redirect_uris":["https://app.example.com/auth/callback"]}` |
| `PUT /api/applications/{id}/oauth` | 同上；`redirect_uris` 字符串数组 | 整份替换，空数组关闭业务应用 OAuth；最多 10 个精确地址，禁止通配符、userinfo、fragment、已有 `code/state/error` 参数；仅 HTTPS，本机 `localhost/127.0.0.1/::1` 允许 HTTP |

`{provider}` 只接受小写 `google`、`github`。`code` 在 `/complete` 中指本地 2FA，在服务商 callback 和下方 exchange 中分别是独立的授权码，不可混用。开始授权的 state 有效期 10 分钟；回调生成的临时完成票据有效期 5 分钟，最多尝试 5 次。Cookie 采用 HttpOnly、部署配置的 Secure、SameSite=Lax；原 `auth_session` 的 SameSite=Strict 保持不变。成功、取消或消费后票据不可重放，过期记录会清理。

## 业务应用 API

本节用于已经创建的业务应用，API Key **仅放在应用服务端**。应用先在「应用管理」登记自己的精确回调地址。此地址接收本站发出的短期授权码，**与管理员在 Google/GitHub 登记的本站服务商回调地址不同**。

### 1. 开始授权

应用服务端生成并保存在用户浏览器会话中的随机 `state` 和 PKCE `code_verifier`。`state` 长 16–256 字节；verifier 使用 43–128 个 `[A-Za-z0-9._~-]` 字符。计算 `code_challenge = BASE64URL(SHA256(code_verifier))`，不含 `=` 填充。

```http
POST /v1/auth/oauth/google/start
X-API-Key: YOUR_SERVER_API_KEY
Content-Type: application/json

{
  "redirect_uri": "https://app.example.com/auth/callback",
  "state": "RANDOM_STATE_BOUND_TO_BROWSER_SESSION",
  "code_challenge": "BASE64URL_SHA256_OF_VERIFIER_43_CHARACTERS_LONG",
  "code_challenge_method": "S256"
}
```

GitHub 使用 `/v1/auth/oauth/github/start`。Key 必须拥有 `auth:login`；创建新用户时额外要求 `auth:register`。App、所有者角色/停用状态、Key 未撤销、scope、精确回调地址会在启动和后续阶段重新核对。

成功 200：

```json
{"authorization_url":"https://auth.example.com/api/auth/oauth/launch?request=ONE_TIME_LAUNCH_TICKET","expires_in":300}
```

让用户浏览器顶层跳转到 `authorization_url`；不要由应用后端跟随跳转，也不要把 API Key 放进 URL。launch 票据一次性使用，5 分钟有效，它为该浏览器建立与服务商 state 绑定的 Cookie，再跳转 Google/GitHub。用户返回本站后会看到应用名称、第三方邮箱和明确确认操作；已开启本地 2FA 时需继续验证。

### 2. 回调与交换

用户确认后浏览器返回已登记地址：

```text
https://app.example.com/auth/callback?code=ONE_TIME_AUTHORIZATION_CODE&state=ORIGINAL_STATE
```

应用必须先严格校验 `state` 与当前浏览器应用会话中的预期值相同并一次性消费该 state。仅由服务端使用**开始授权时的同一把 Key**交换：

```http
POST /v1/auth/oauth/exchange
X-API-Key: YOUR_SERVER_API_KEY
Content-Type: application/json

{
  "code": "ONE_TIME_AUTHORIZATION_CODE",
  "code_verifier": "ORIGINAL_SERVER_SIDE_PKCE_VERIFIER",
  "redirect_uri": "https://app.example.com/auth/callback"
}
```

本站授权码有效期 **60 秒、一次使用**，绑定原始 Key、应用、回调地址、PKCE challenge、服务商配置和用户认证状态。另一应用、另一把同应用 Key、不同回调地址或错误 verifier 均不能交换。用户取消绑定、改变本地密码/2FA、被停用、Key 被撤销、应用所有者失去开发者/管理员资格或回调地址被移除时，待处理授权失效。失败或取消授权留在本站显示错误，不重定向到应用的任意错误 URL。

成功 200 与密码登录保持一致：

```json
{
  "access_token": "APPLICATION_SESSION_TOKEN",
  "token_type": "Bearer",
  "expires_in": 86400,
  "user": {"id":"...","email":"...","name":"...","role":"user","disabled":false,"tfa":false,"created_at":0}
}
```

业务应用流程不会设置新的控制台管理 Cookie。后续使用现有 `/v1/session`、`/v1/session/heartbeat` 和 `/v1/auth/logout`，同时携带应用 Key 与 Bearer Token；会话不能跨应用使用，不能用于控制台管理接口。回调页避免加载第三方资源，消费授权码后立即清理 URL，并设置 `Referrer-Policy: no-referrer`，不要记录授权码或 Token。

### Node.js 服务端 PKCE 示例

以下示例省略框架路由和会话中间件；`req.session` 必须是与浏览器绑定的服务端会话，不能将 verifier 或 API Key 返回给前端。

```js
import { randomBytes, createHash, timingSafeEqual } from 'node:crypto';
const authURL = 'https://auth.example.com';
const callback = 'https://app.example.com/auth/callback';
const key = process.env.AUTH_APP_API_KEY;

// GET /login/google
const state = randomBytes(32).toString('base64url');
const verifier = randomBytes(32).toString('base64url');
req.session.oauth = { state, verifier, createdAt: Date.now() };
const response = await fetch(`${authURL}/v1/auth/oauth/google/start`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', 'X-API-Key': key },
  body: JSON.stringify({ redirect_uri: callback, state,
    code_challenge: createHash('sha256').update(verifier).digest('base64url'),
    code_challenge_method: 'S256' })
});
if (!response.ok) throw new Error('无法开始授权');
res.redirect((await response.json()).authorization_url);

// GET /auth/callback (separate route)
const pending = req.session.oauth;
delete req.session.oauth; // once per browser flow
const received = Buffer.from(typeof req.query.state === 'string' ? req.query.state : '');
const expected = Buffer.from(pending?.state || '');
if (!pending || Date.now() - pending.createdAt > 20 * 60_000 ||
    received.length !== expected.length || !timingSafeEqual(received, expected) ||
    typeof req.query.code !== 'string') throw new Error('授权状态无效');
const exchange = await fetch(`${authURL}/v1/auth/oauth/exchange`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', 'X-API-Key': key },
  body: JSON.stringify({ code: req.query.code, code_verifier: pending.verifier,
    redirect_uri: callback })
});
if (!exchange.ok) throw new Error('授权码交换失败，请重新登录');
req.session.auth = await exchange.json(); // keep the Bearer Token server-side
res.redirect('/');
```

## 错误、审计和升级

- 400：JSON/参数不合法，密码长度、姓名、PKCE 或回调地址格式错误。
- 401：无有效会话/Key，授权 state、票据或授权码失效/重放，2FA 错误。2FA 提示附 `requires_2fa:true`。
- 403：管理权限、应用归属、scope 或重新验证密码/2FA 不满足。
- 404：不支持的服务商，或不存在绑定。
- 409：未启用服务、绑定唯一性冲突、邮箱冲突、配置 revision 冲突。
- 429：认证请求过于频繁。

审计动作新增 `oauth.configure`、`oauth.bind`、`oauth.unbind`、`oauth.redirects`；成功登录和注册继续记录 `auth.login`、`auth.register`，统计沿用已有机制。审计只记录服务名称/应用 ID，不记录 Secret、Token、verifier、state 或用户提交的验证码。

数据库结构 **2 → 3** 新增独立表，不改变原用户的密码、邮箱、角色、会话、2FA、资料或主密钥。迁移在事务中完成，可重复执行，保留原统计不重复回填。结构 1 也可直接迁移至 3。旧版部署代理仅支持结构 1/2，首次升级本功能需使用**完整新安装包**在原目录更新代理和工具：

```sh
sudo env AUTH_ALLOW_SCHEMA_UPGRADE=true ./authctl.sh update
# 原源码部署首次接管：
sudo env AUTH_ALLOW_SCHEMA_UPGRADE=true ./authctl.sh adopt
```

升级前工具停服备份，更新后禁止回退结构 1/2 镜像；跨结构健康检查失败只允许向前恢复兼容镜像。保留 `.env`、`.deployment/`、备份和数据卷；不要覆盖数据库或删除 `master.key`。后续兼容结构内的版本更新继续在管理员网页完成。
