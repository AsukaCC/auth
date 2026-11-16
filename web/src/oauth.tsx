import { useEffect, useState } from "react";
import { Github, Globe, Link2, LoaderCircle, ShieldCheck, X } from "lucide-react";

type API = (path: string, method?: string, data?: unknown) => Promise<any>;
type Provider = "google" | "github";
const names: Record<Provider, string> = { google: "Google", github: "GitHub" };
const providers: Provider[] = ["google", "github"];
const errors: Record<string, string> = {
  invalid_state: "授权已过期或与当前浏览器不匹配，请重新开始。",
  config_changed: "登录配置已变更，请重新开始。",
  cancelled: "已取消第三方授权。",
  provider_failed: "第三方授权失败，请确认该身份的主邮箱已验证后重试。",
  binding_required: "此邮箱已有账户，请先使用原账户登录，再到「账户安全」绑定第三方身份。",
  registration_disabled: "此身份尚未绑定账户，且当前未开放第三方注册。请先登录已有账户进行绑定。",
  unavailable: "账户不可用，请联系管理员。",
};
export function oauthReturnError(code: string) { return errors[code] || "第三方授权未完成，请重试。"; }
function ProviderIcon({ provider }: { provider: Provider }) {
  return provider === "github" ? <Github size={19} aria-hidden="true" /> : <Globe size={19} aria-hidden="true" />;
}
function Alert({ error }: { error: string }) { return error ? <div className="alert error" role="alert">{error}</div> : null; }
function Credentials({ tfa, password, code, onPassword, onCode }: { tfa: boolean; password: string; code: string; onPassword: (s: string) => void; onCode: (s: string) => void }) {
  return <>
    <label className="field"><span>当前账户密码</span><input type="password" autoComplete="current-password" required maxLength={72} value={password} onChange={e => onPassword(e.target.value)} /></label>
    {tfa && <label className="field"><span>两步验证码或备用码</span><input autoComplete="one-time-code" required maxLength={20} value={code} onChange={e => onCode(e.target.value)} /></label>}
  </>;
}

export function OAuthLogin({ api }: { api: API }) {
  const [available, setAvailable] = useState<Provider[]>([]);
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  useEffect(() => { let active = true; api("/api/auth/oauth/providers").then(data => { if (active) setAvailable(data.providers.map((p: { provider: Provider }) => p.provider)); }).catch(() => { if (active) setError("第三方登录入口加载失败，请刷新重试。"); }); return () => { active = false; }; }, [api]);
  if (!available.length) return <Alert error={error} />;
  return <div className="oauth-login"><div className="oauth-divider"><span>或使用第三方身份</span></div><Alert error={error} /><div className="oauth-login-buttons">{available.map(provider => <button key={provider} className="button secondary" disabled={busy} onClick={async () => {
    setBusy(true); setError("");
    try { const result = await api(`/api/auth/oauth/${provider}/start`, "POST", { intent: "login" }); location.assign(result.authorization_url); }
    catch (e) { setError((e as Error).message); setBusy(false); }
  }}><ProviderIcon provider={provider} />{names[provider]} 登录</button>)}</div></div>;
}

type Pending = { provider: Provider; intent: string; email: string; name: string; requires_2fa: boolean; requires_registration: boolean; application: string };
export function OAuthCompletion({ api, onDone, onCancel }: { api: API; onDone: (bound: boolean) => Promise<void>; onCancel: () => void }) {
  const [pending, setPending] = useState<Pending | null>(null), [error, setError] = useState("");
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false);
  const [name, setName] = useState(""), [password, setPassword] = useState(""), [code, setCode] = useState("");
  useEffect(() => { let active = true; api("/api/auth/oauth/pending").then(p => { if (active) { setPending(p); setName(p.name); } }).catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [api]);
  const title = pending?.intent === "bind" ? "确认绑定第三方身份" : pending?.requires_registration ? "完成账户注册" : "确认第三方登录";
  return <main className="oauth-completion-layout"><section className="panel oauth-completion" aria-labelledby="oauth-title">
    <img src="/brand-mark.svg" alt="" width="48" height="48" /><h1 id="oauth-title">{title}</h1>
    <Alert error={error} />
    {loading && <p role="status"><LoaderCircle className="spin" size={18} /> 正在验证授权…</p>}
    {pending && <><p>已验证 {names[pending.provider]} 身份 <strong>{pending.email}</strong>。</p>
      {pending.application && <div className="alert info">你正在登录应用「{pending.application}」。确认后会返回该应用，由其服务端交换专属会话。</div>}
      {pending.intent === "bind" && <p>将此身份绑定到当前登录的 Auth Center 账户，之后可用于登录。</p>}
      {pending.requires_registration && <p>设置本地密码，方便后续管理账户安全和找回账户。新账户的角色为普通用户。</p>}
      <form onSubmit={async e => {
        e.preventDefault(); if (busy) return; setBusy(true); setError("");
        try { const result = await api("/api/auth/oauth/complete", "POST", { name, password, code }); if (result.redirect_url) { location.assign(result.redirect_url); return; } await onDone(!!result.bound); }
        catch (e) { setError((e as Error).message); }
        finally { setBusy(false); setPassword(""); setCode(""); }
      }}>
        {pending.requires_registration && <><label className="field"><span>姓名</span><input required maxLength={80} autoComplete="name" value={name} onChange={e => setName(e.target.value)} /></label><label className="field"><span>本地密码（10–72 字节）</span><input required type="password" minLength={10} maxLength={72} autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} /></label></>}
        {pending.requires_2fa && <label className="field"><span>两步验证码或备用码</span><input required autoComplete="one-time-code" maxLength={20} value={code} onChange={e => setCode(e.target.value)} /></label>}
        <button className="button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <ShieldCheck size={17} />}{pending.intent === "bind" ? "确认绑定" : pending.requires_registration ? "注册并继续" : "确认登录"}</button>
      </form></>}
    <button className="text-button" disabled={busy} onClick={async () => { setBusy(true); try { await api("/api/auth/oauth/cancel", "POST"); onCancel(); } catch (e) { setError((e as Error).message); setBusy(false); } }}>取消并返回</button>
  </section></main>;
}

type Identity = { provider: Provider; email: string; created_at: number };
export function OAuthBindings({ api, tfa, onChanged }: { api: API; tfa: boolean; onChanged: () => Promise<void> }) {
  const [identities, setIdentities] = useState<Identity[]>([]), [enabled, setEnabled] = useState<Provider[]>([]);
  const [error, setError] = useState(""), [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [message, setMessage] = useState("");
  const [action, setAction] = useState<{ provider: Provider; unlink: boolean } | null>(null);
  const [password, setPassword] = useState(""), [code, setCode] = useState("");
  const load = async () => { const [links, config] = await Promise.all([api("/api/account/identities"), api("/api/auth/oauth/providers")]); setIdentities(links.identities); setEnabled(config.providers.map((p: { provider: Provider }) => p.provider)); };
  useEffect(() => { void load().catch(e => setError(e.message)).finally(() => setLoading(false)); }, [api]);
  return <section className="panel oauth-bindings"><div className="panel-title"><div><h2>第三方账户绑定</h2><p>绑定 Google 或 GitHub，使用同一个 Auth Center 账户登录。</p></div><Link2 size={23} /></div><Alert error={error} />{message && <div className="alert info" role="status">{message}</div>}
    {loading ? <p role="status">正在读取绑定…</p> : <div className="oauth-identity-list">{providers.map(provider => { const identity = identities.find(i => i.provider === provider); return <div className="oauth-identity" key={provider}><div className="feature-icon blue"><ProviderIcon provider={provider} /></div><div className="grow"><b>{names[provider]}</b><p>{identity ? identity.email : enabled.includes(provider) ? "尚未绑定" : "管理员尚未启用此服务"}</p></div><button className="button secondary" disabled={busy || !identity && !enabled.includes(provider)} onClick={() => { setAction({ provider, unlink: !!identity }); setPassword(""); setCode(""); setError(""); setMessage(""); }}>{identity ? "解绑" : "绑定"}</button></div>; })}</div>}
    <p className="muted">第三方邮箱可以与登录邮箱不同。相同邮箱不会自动合并账户；解绑会结束其他设备和应用会话，当前会话保留。</p>
    {action && <form className="oauth-action" onSubmit={async e => {
      e.preventDefault(); if (busy) return; setBusy(true); setError("");
      try {
        if (action.unlink) { await api(`/api/account/identities/${action.provider}/unlink`, "POST", { password, code }); setAction(null); setMessage("已解除绑定，并结束其他会话。"); await load(); await onChanged(); }
        else { const result = await api(`/api/auth/oauth/${action.provider}/start`, "POST", { intent: "bind", password, code }); location.assign(result.authorization_url); }
      } catch (e) { setError((e as Error).message); }
      finally { setBusy(false); setPassword(""); setCode(""); }
    }}><h3>{action.unlink ? "解除" : "添加"} {names[action.provider]} 绑定</h3><Credentials tfa={tfa} password={password} code={code} onPassword={setPassword} onCode={setCode} /><div className="form-actions"><button className="button" disabled={busy}>{busy && <LoaderCircle className="spin" size={16} />}{action.unlink ? "确认解绑" : "验证并前往授权"}</button><button type="button" className="button secondary" disabled={busy} onClick={() => { setAction(null); setPassword(""); setCode(""); }}>取消</button></div></form>}
  </section>;
}

type Configuration = { provider: Provider; enabled: boolean; client_id: string; has_secret: boolean; allow_signup: boolean; revision: string; callback_url: string };
function ProviderSettings({ api, config, tfa, onSaved }: { api: API; config: Configuration; tfa: boolean; onSaved: () => Promise<void> }) {
  const [enabled, setEnabled] = useState(config.enabled), [clientID, setClientID] = useState(config.client_id), [secret, setSecret] = useState("");
  const [signup, setSignup] = useState(config.allow_signup), [password, setPassword] = useState(""), [code, setCode] = useState("");
  const [error, setError] = useState(""), [busy, setBusy] = useState(false), [message, setMessage] = useState("");
  return <section className="panel oauth-provider-settings"><div className="panel-title"><h2><ProviderIcon provider={config.provider} /> {names[config.provider]}</h2><a className="text-button" href={config.provider === "google" ? "https://console.cloud.google.com/apis/credentials" : "https://github.com/settings/developers"} target="_blank" rel="noreferrer">创建 OAuth 应用 ↗</a></div>
    <p>{config.provider === "google" ? "在 Google Cloud 创建 Web 应用 OAuth 客户端，并配置同意屏幕；测试阶段需加入测试用户。" : "在 GitHub Developer settings 创建 OAuth App，填写站点首页与以下回调地址。"}</p>
    <label className="field"><span>授权回调地址（精确登记）</span><input readOnly value={config.callback_url} onFocus={e => e.target.select()} /></label><Alert error={error} />{message && <p role="status">{message}</p>}
    <form onSubmit={async e => { e.preventDefault(); if (busy) return; setBusy(true); setError(""); setMessage(""); try { await api(`/api/admin/oauth/${config.provider}`, "PUT", { enabled, client_id: clientID, client_secret: secret, allow_signup: signup, expected_revision: config.revision, password, code }); setMessage("配置已保存。"); await onSaved(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); setPassword(""); setCode(""); setSecret(""); } }}>
      <label className="scope-option"><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} /><span>启用 {names[config.provider]} 登录和绑定</span></label>
      <label className="field"><span>Client ID</span><input required={enabled} autoComplete="off" maxLength={500} value={clientID} onChange={e => setClientID(e.target.value)} /></label>
      <label className="field"><span>Client Secret{config.has_secret ? "（已保存，留空保留）" : ""}</span><input type="password" required={enabled && !config.has_secret} autoComplete="new-password" maxLength={2000} value={secret} onChange={e => setSecret(e.target.value)} /></label>
      <label className="scope-option"><input type="checkbox" checked={signup} onChange={e => setSignup(e.target.checked)} /><span>允许首次授权的身份注册普通用户账户</span></label>
      <p className="muted">未开放注册时，仅已绑定身份可以登录。关闭服务保留原有绑定；修改配置将使进行中的授权失效。</p>
      <Credentials tfa={tfa} password={password} code={code} onPassword={setPassword} onCode={setCode} />
      <button className="button" disabled={busy}>{busy && <LoaderCircle className="spin" size={16} />}保存配置</button>
    </form>
  </section>;
}
export function OAuthAdmin({ api, tfa }: { api: API; tfa: boolean }) {
  const [configs, setConfigs] = useState<Configuration[]>([]), [error, setError] = useState(""), [loading, setLoading] = useState(true);
  const [message, setMessage] = useState("");
  const load = async () => { const result = await api("/api/admin/oauth"); setConfigs(result.providers); };
  useEffect(() => { void load().catch(e => setError(e.message)).finally(() => setLoading(false)); }, [api]);
  return <div className="oauth-admin"><div className="alert info">生产环境请先设置实际的 AUTH_PUBLIC_URL 与 HTTPS。Client Secret 加密保存，接口不返回已保存的明文；管理员修改配置需要重新验证身份。</div><Alert error={error} />{message && <div className="alert info" role="status">{message}</div>}{loading && <p role="status">正在读取登录服务配置…</p>}<div className="oauth-provider-grid">{configs.map(config => <ProviderSettings key={`${config.provider}-${config.revision}`} api={api} config={config} tfa={tfa} onSaved={async () => { await load(); setMessage(`${names[config.provider]} 配置已保存。`); }} />)}</div></div>;
}

export function ApplicationOAuthSettings({ api, appID }: { api: API; appID: string }) {
  const [uris, setURIs] = useState(""), [error, setError] = useState(""), [message, setMessage] = useState("");
  const [loading, setLoading] = useState(true), [busy, setBusy] = useState(false), [opened, setOpened] = useState(false);
  useEffect(() => { if (!opened) return; let active = true; api(`/api/applications/${appID}/oauth`).then(data => { if (active) setURIs(data.redirect_uris.join("\n")); }).catch(e => { if (active) setError(e.message); }).finally(() => { if (active) setLoading(false); }); return () => { active = false; }; }, [api, appID, opened]);
  return <div className="oauth-application-settings"><button className="text-button" aria-expanded={opened} onClick={() => setOpened(!opened)}><Link2 size={15} />第三方登录回调地址{opened && <X size={14} />}</button>{opened && <><Alert error={error} />{message && <p role="status">{message}</p>}{loading ? <p role="status">正在读取…</p> : <form onSubmit={async e => { e.preventDefault(); setBusy(true); setError(""); setMessage(""); try { await api(`/api/applications/${appID}/oauth`, "PUT", { redirect_uris: uris.split("\n").map(u => u.trim()).filter(Boolean) }); setMessage("回调地址已保存。"); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }}><label className="field"><span>应用回调地址，每行一个，最多 10 个</span><textarea rows={3} value={uris} onChange={e => setURIs(e.target.value)} placeholder="https://app.example.com/auth/callback" /></label><p className="muted">需精确匹配，禁止通配符；本机开发允许 localhost / 127.0.0.1 的 HTTP 地址。服务商的授权回调地址在管理员配置中填写。</p><button className="button secondary" disabled={busy}>{busy ? "保存中…" : "保存回调地址"}</button></form>}</>}</div>;
}

export function OAuthDocs() {
  return <section className="panel docs-card"><h2>Google / GitHub 登录与账户绑定</h2><p>管理员在「第三方登录」配置服务商 Client ID、Client Secret 和回调地址；账户持有人可在「账户安全」绑定和解绑。相同邮箱不会自动合并，已有账户必须先登录后绑定。</p><h3>业务应用授权码流程</h3><ol><li>在「应用管理」登记精确回调地址。在服务端生成随机 state 和 PKCE verifier（43–128 位），计算 BASE64URL(SHA256(verifier))。</li><li>服务端携带具有 auth:login 权限的 X-API-Key，POST /v1/auth/oauth/google/start 或 /v1/auth/oauth/github/start，提交 redirect_uri、state、code_challenge 和 code_challenge_method: S256。</li><li>将用户浏览器跳转到返回的 authorization_url。用户在身份中心确认登录，已启用 2FA 时仍需验证码；注册还要求服务商开放注册及 Key 拥有 auth:register 权限。</li><li>应用回调校验与原浏览器会话绑定的 state，再由服务端 POST /v1/auth/oauth/exchange，提交 code、code_verifier、redirect_uri 并携带原 X-API-Key。授权码 60 秒有效、仅使用一次。</li><li>交换成功返回 access_token、token_type、expires_in、user，与密码登录一致。后续调用 /v1/session 时继续携带该应用的 X-API-Key 与 Bearer Token。</li></ol><p>API Key、verifier 和 Bearer Token 保留在应用服务端。授权码绑定原始 Key、应用及精确回调地址；撤销 Key、停用所有者、取消绑定或变更认证配置均会使待处理授权失效。业务应用不会获取身份中心的管理 Cookie。</p></section>;
}
