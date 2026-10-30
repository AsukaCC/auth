import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Shield,
  LayoutDashboard,
  AppWindow,
  KeyRound,
  Monitor,
  LockKeyhole,
  Mail,
  Users,
  LifeBuoy,
  ScrollText,
  LogOut,
  ArrowUpRight,
  Plus,
  ChevronRight,
  Check,
  Copy,
  X,
  LoaderCircle,
  Search,
  Menu,
  Code2,
  Clock3,
  Fingerprint,
  Activity,
  Terminal,
  ShieldCheck,
} from "lucide-react";
import { QRCodeSVG } from "qrcode.react";
import "./style.css";

type User = {
  id: string;
  email: string;
  name: string;
  role: string;
  tfa: boolean;
  disabled: boolean;
  created_at: number;
};
type Session = {
  id: string;
  app_id: string;
  app_name: string;
  ip: string;
  agent: string;
  last_seen: number;
  created_at: number;
  expires_at: number;
  online: boolean;
  current: boolean;
};
type Application = {
  id: string;
  name: string;
  description: string;
  key_count: number;
};
type APIKey = {
  id: string;
  app_name: string;
  name: string;
  prefix: string;
  scopes: string[];
  revoked: boolean;
  last_used: number;
};
type Audit = {
  action: string;
  detail: string;
  email: string;
  ip: string;
  created_at: number;
};
type Appeal = {
  id: string;
  email: string;
  contact: string;
  reason: string;
  status: string;
  note: string;
  created_at: number;
};
type MailConfig = {
  enabled: boolean;
  host: string;
  port: number;
  username: string;
  from: string;
  tls: string;
  has_password: boolean;
};
type Page =
  | "overview"
  | "sessions"
  | "security"
  | "applications"
  | "keys"
  | "users"
  | "mail"
  | "appeals"
  | "audit"
  | "docs";
type Modal = { type: string; data?: any };
const labels: Record<Page, string> = {
  overview: "概览",
  sessions: "登录会话",
  security: "账户安全",
  applications: "应用管理",
  keys: "API 密钥",
  users: "用户与权限",
  mail: "邮件服务",
  appeals: "找回申诉",
  audit: "审计日志",
  docs: "接入文档",
};
const icons: Record<Page, React.ElementType> = {
  overview: LayoutDashboard,
  sessions: Monitor,
  security: ShieldCheck,
  applications: AppWindow,
  keys: KeyRound,
  users: Users,
  mail: Mail,
  appeals: LifeBuoy,
  audit: ScrollText,
  docs: Code2,
};
const roleLabel: Record<string, string> = {
  admin: "管理员",
  developer: "开发者",
  user: "普通用户",
};
const actionLabel: Record<string, string> = {
  "auth.login": "账户登录",
  "auth.register": "新账户注册",
  "password.change": "修改密码",
  "password.recover": "密码找回",
  "2fa.enable": "启用两步验证",
  "2fa.disable": "关闭两步验证",
  "application.create": "创建应用",
  "key.create": "创建 API 密钥",
  "key.revoke": "撤销 API 密钥",
  "session.revoke": "撤销登录会话",
  "mail.update": "更新邮件配置",
  "user.update": "修改用户权限",
  "appeal.approved": "批准找回申诉",
  "appeal.rejected": "拒绝找回申诉",
  "recovery.mail_failed": "找回邮件发送失败",
};
const time = (n: number) =>
  n
    ? new Date(n * 1000).toLocaleString("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      })
    : "尚未使用";
const ago = (n: number) => {
  const s = Math.max(0, Math.floor(Date.now() / 1000) - n);
  return s < 60
    ? "刚刚"
    : s < 3600
      ? `${Math.floor(s / 60)} 分钟前`
      : s < 86400
        ? `${Math.floor(s / 3600)} 小时前`
        : time(n);
};
const fields = (e: React.FormEvent<HTMLFormElement>) =>
  Object.fromEntries(new FormData(e.currentTarget));
async function api(path: string, method = "GET", data?: unknown) {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: data ? { "Content-Type": "application/json" } : undefined,
    body: data ? JSON.stringify(data) : undefined,
  });
  const body = await res.json();
  if (!res.ok) {
    const err = new Error(body.error || "请求失败") as Error & {
      status: number;
    };
    err.status = res.status;
    throw err;
  }
  return body;
}
function Button({
  children,
  secondary = false,
  danger = false,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  secondary?: boolean;
  danger?: boolean;
}) {
  return (
    <button
      className={`button ${secondary ? "secondary" : ""} ${danger ? "danger" : ""}`}
      {...props}
    >
      {children}
    </button>
  );
}
function Empty({
  icon: Icon = AppWindow,
  title,
  description,
  action,
}: {
  icon?: React.ElementType;
  title: string;
  description: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="empty">
      <div className="empty-icon">
        <Icon size={26} />
      </div>
      <h3>{title}</h3>
      <p>{description}</p>
      {action}
    </div>
  );
}
function Badge({
  children,
  tone = "gray",
}: {
  children: React.ReactNode;
  tone?: string;
}) {
  return <span className={`badge ${tone}`}>{children}</span>;
}
function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: React.ReactNode;
  hint?: string;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint && <small>{hint}</small>}
    </label>
  );
}
function ModalShell({
  title,
  close,
  children,
}: {
  title: string;
  close: () => void;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement;
    ref.current?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
      if (e.key === "Tab") {
        const list = ref.current?.querySelectorAll<HTMLElement>(
          "button,input,select,textarea,a[href]",
        );
        if (!list?.length) return;
        const first = list[0],
          last = list[list.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("keydown", key);
      prev?.focus();
    };
  }, []);
  return (
    <div
      className="modal-overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) close();
      }}
    >
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        ref={ref}
        tabIndex={-1}
      >
        <div className="modal-heading">
          <h2>{title}</h2>
          <button className="icon-button" onClick={close} aria-label="关闭">
            <X size={20} />
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
function App() {
  const [status, setStatus] = useState<{
      setup_required: boolean;
      mail_enabled: boolean;
    } | null>(null),
    [user, setUser] = useState<User | null>(null),
    [loading, setLoading] = useState(true),
    [page, setPage] = useState<Page>("overview"),
    [toast, setToast] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [modal, setModal] = useState<Modal | null>(null),
    [authMode, setAuthMode] = useState("login"),
    [sidebar, setSidebar] = useState(false);
  const [sessions, setSessions] = useState<Session[]>([]),
    [apps, setApps] = useState<Application[]>([]),
    [keys, setKeys] = useState<APIKey[]>([]),
    [users, setUsers] = useState<User[]>([]),
    [appeals, setAppeals] = useState<Appeal[]>([]),
    [audits, setAudits] = useState<Audit[]>([]),
    [mail, setMail] = useState<MailConfig | null>(null),
    [search, setSearch] = useState(""),
    [refreshing, setRefreshing] = useState(false);
  const resetToken = useRef(
    new URLSearchParams(location.search).get("reset") || "",
  );
  const privileged = user?.role === "admin" || user?.role === "developer";
  const notify = (s: string) => setToast(s);
  async function refresh() {
    const current: User = await api("/api/me");
    setUser(current);
    const tasks = [
      api("/api/sessions").then(setSessions),
      api("/api/audit").then(setAudits),
    ];
    if (current.role !== "user")
      tasks.push(
        api("/api/applications").then(setApps),
        api("/api/keys").then(setKeys),
      );
    else {
      setApps([]);
      setKeys([]);
    }
    if (current.role === "admin")
      tasks.push(
        api("/api/admin/users").then(setUsers),
        api("/api/admin/appeals").then(setAppeals),
        api("/api/admin/mail").then(setMail),
      );
    await Promise.all(tasks);
  }
  useEffect(() => {
    (async () => {
      try {
        setStatus(await api("/api/status"));
        try {
          await refresh();
        } catch (e) {
          if ((e as any).status !== 401) throw e;
        }
        if (resetToken.current) {
          setAuthMode("reset");
          setUser(null);
          history.replaceState({}, "", location.pathname);
        }
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setLoading(false);
      }
    })();
  }, []);
  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(""), 4500);
    return () => clearTimeout(t);
  }, [toast]);
  useEffect(() => {
    if (!user) return;
    let cancelled = false;
    const beat = async () => {
      try {
        await api("/api/heartbeat", "POST");
        const rows = await api("/api/sessions");
        if (!cancelled) setSessions(rows);
      } catch (e) {
        if ((e as any).status === 401) {
          setUser(null);
          setError("登录已失效，请重新登录");
        }
      }
    };
    void beat();
    const t = setInterval(beat, 30000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [user?.id]);
  useEffect(() => {
    setSearch("");
    setSidebar(false);
    setError("");
  }, [page]);
  async function run(task: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await task();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const open = (type: string, data?: any) => {
    setError("");
    setModal({ type, data });
  };
  const close = () => {
    if (!busy) {
      setModal(null);
      setError("");
    }
  };
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      notify("已复制到剪贴板");
    } catch {
      setError("复制失败，请手动选择并复制");
    }
  };
  if (loading)
    return (
      <div className="loading">
        <Shield size={32} />
        <LoaderCircle className="spin" />
        正在连接身份服务…
      </div>
    );
  if (!user) {
    const setup = status?.setup_required && authMode !== "reset";
    return (
      <div className="auth-layout">
        <aside className="auth-story">
          <a className="brand" href="/">
            <div className="brand-mark">
              <Shield size={24} />
            </div>
            auth<span>身份管理平台</span>
          </a>
          <div className="story-content">
            <div className="eyebrow">ONE IDENTITY. EVERY APPLICATION.</div>
            <h1>
              每一次访问，
              <br />
              都值得信任。
            </h1>
            <p>
              统一身份，连接应用。
              <br />
              让账户安全成为你业务的可靠起点。
            </p>
            <div className="orbit">
              <div className="orbit-ring ring-one" />
              <div className="orbit-ring ring-two" />
              <div className="orbit-center">
                <Fingerprint size={66} strokeWidth={1.2} />
              </div>
              <div className="orbit-chip chip-one">
                <AppWindow size={22} />
              </div>
              <div className="orbit-chip chip-two">
                <KeyRound size={22} />
              </div>
              <div className="orbit-chip chip-three">
                <LockKeyhole size={22} />
              </div>
            </div>
          </div>
          <div className="story-bottom">
            <span className="status-dot" />
            统一认证 · 安全连接
          </div>
        </aside>
        <main className="auth-main">
          <div className="auth-box">
            <Badge tone="blue">AUTH CENTER</Badge>
            <h2>
              {setup
                ? "创建你的管理空间"
                : authMode === "login"
                  ? "欢迎回来"
                  : authMode === "forgot"
                    ? "找回账户密码"
                    : authMode === "appeal"
                      ? "向管理员申诉"
                      : "设置新密码"}
            </h2>
            <p className="muted">
              {setup
                ? "设置首位管理员，开始连接你的应用。"
                : authMode === "login"
                  ? "登录以管理你的账户、应用与访问权限。"
                  : authMode === "forgot"
                    ? "通过绑定邮箱接收安全的密码重置链接。"
                    : authMode === "appeal"
                      ? "请提供可核实的信息，管理员将与你联系。"
                      : "链接有效期为 30 分钟，且只能使用一次。"}
            </p>
            {error && (
              <div className="alert error" role="alert">
                {error}
              </div>
            )}
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const f = fields(e);
                void run(async () => {
                  if (setup) {
                    await api("/api/setup", "POST", f);
                    setStatus({ ...status!, setup_required: false });
                    await refresh();
                  } else if (authMode === "login") {
                    await api("/api/auth/login", "POST", f);
                    await refresh();
                  } else if (authMode === "forgot") {
                    const b = await api("/api/auth/forgot", "POST", f);
                    notify(b.message);
                  } else if (authMode === "appeal") {
                    const b = await api("/api/auth/appeal", "POST", f);
                    notify(b.message);
                    setAuthMode("login");
                  } else {
                    const b = await api("/api/auth/reset", "POST", {
                      ...f,
                      token: resetToken.current,
                    });
                    notify(b.message);
                    resetToken.current = "";
                    setAuthMode("login");
                  }
                });
              }}
              key={`${setup}-${authMode}`}
            >
              {authMode !== "reset" && (
                <Field label="账户邮箱">
                  <input
                    name="email"
                    type="email"
                    placeholder="name@company.com"
                    required
                    autoComplete="username"
                  />
                </Field>
              )}
              {setup && (
                <Field label="管理员姓名">
                  <input
                    name="name"
                    placeholder="你的姓名"
                    maxLength={80}
                    required
                  />
                </Field>
              )}
              {(setup || authMode === "login" || authMode === "reset") && (
                <Field
                  label={authMode === "reset" ? "新密码" : "密码"}
                  hint={
                    setup || authMode === "reset"
                      ? "至少 10 个字符，最多 72 字节"
                      : undefined
                  }
                >
                  <input
                    type="password"
                    name="password"
                    minLength={setup || authMode === "reset" ? 10 : undefined}
                    maxLength={72}
                    placeholder="输入密码"
                    autoComplete={
                      setup || authMode === "reset"
                        ? "new-password"
                        : "current-password"
                    }
                    required
                  />
                </Field>
              )}
              {!setup && authMode === "login" && (
                <Field label="两步验证码（已启用时填写）">
                  <input
                    name="code"
                    placeholder="6 位验证码或备用恢复码"
                    autoComplete="one-time-code"
                    maxLength={20}
                  />
                </Field>
              )}
              {!setup && authMode === "appeal" && (
                <>
                  <Field label="可联系的方式">
                    <input
                      name="contact"
                      placeholder="备用邮箱 / 电话 / 其他联系方式"
                      required
                      maxLength={300}
                    />
                  </Field>
                  <Field label="申诉说明">
                    <textarea
                      name="reason"
                      placeholder="说明账户使用情况及需要找回的原因"
                      required
                      maxLength={2000}
                      rows={4}
                    />
                  </Field>
                </>
              )}
              <Button type="submit" disabled={busy}>
                {busy ? <LoaderCircle size={17} className="spin" /> : null}
                {setup
                  ? "创建管理员账户"
                  : authMode === "login"
                    ? "登录控制台"
                    : authMode === "forgot"
                      ? "发送找回邮件"
                      : authMode === "appeal"
                        ? "提交申诉"
                        : "重置密码"}
                <ArrowUpRight size={17} />
              </Button>
            </form>
            {!setup && (
              <div className="auth-links">
                {authMode === "login" ? (
                  <button
                    onClick={() => {
                      setError("");
                      setAuthMode(status?.mail_enabled ? "forgot" : "appeal");
                    }}
                  >
                    忘记密码？{!status?.mail_enabled ? "联系管理员找回" : ""}
                  </button>
                ) : (
                  <button
                    onClick={() => {
                      setError("");
                      setAuthMode("login");
                    }}
                  >
                    返回登录
                  </button>
                )}
                {authMode === "forgot" && (
                  <button
                    onClick={() => {
                      setError("");
                      setAuthMode("appeal");
                    }}
                  >
                    无法收取邮件？提交申诉
                  </button>
                )}
              </div>
            )}
            <div className="auth-note">
              <ShieldCheck size={15} />
              你的身份信息始终受到保护
            </div>
          </div>
          <footer>
            AUTH CENTER <span>统一身份与访问管理</span>
          </footer>
        </main>
        {toast && (
          <div className="toast" role="status">
            <Check size={18} />
            {toast}
          </div>
        )}
      </div>
    );
  }
  const visibleNav: Page[] = [
    "overview",
    "sessions",
    "security",
    ...(privileged ? (["applications", "keys"] as Page[]) : []),
    ...(user.role === "admin" ? (["users", "mail", "appeals"] as Page[]) : []),
    "audit",
    "docs",
  ];
  const filtered = <T,>(items: T[], pick: (item: T) => string) =>
    items.filter((item) =>
      pick(item).toLowerCase().includes(search.toLowerCase()),
    );
  const online = sessions.filter((s) => s.online).length,
    pending = appeals.filter((a) => a.status === "pending").length;
  const submit =
    (task: (f: Record<string, FormDataEntryValue>) => Promise<void>) =>
    (e: React.FormEvent<HTMLFormElement>) => {
      e.preventDefault();
      const f = fields(e);
      void run(() => task(f));
    };
  return (
    <div className="app-shell">
      <aside className={`sidebar ${sidebar ? "mobile-open" : ""}`}>
        <a
          href="#"
          onClick={(e) => {
            e.preventDefault();
            setPage("overview");
          }}
          className="brand"
        >
          <div className="brand-mark">
            <Shield size={23} />
          </div>
          auth<span>管理控制台</span>
        </a>
        <div className="workspace">
          <div className="workspace-avatar">A</div>
          <div>
            <b>Auth 工作空间</b>
            <small>身份与访问管理</small>
          </div>
          <ChevronRight size={16} />
        </div>
        <div className="nav-label">工作空间</div>
        <nav>
          {visibleNav.map((p) => {
            const Icon = icons[p];
            return (
              <React.Fragment key={p}>
                {p === "applications" && (
                  <div className="nav-label nav-divider">开发与管理</div>
                )}
                {p === "audit" && (
                  <div className="nav-label nav-divider">更多</div>
                )}
                <button
                  className={page === p ? "active" : ""}
                  onClick={() => setPage(p)}
                >
                  <Icon size={18} />
                  <span>{labels[p]}</span>
                  {p === "appeals" && pending > 0 && (
                    <b className="nav-count">{pending}</b>
                  )}
                  {p === "docs" && <ArrowUpRight size={14} />}
                </button>
              </React.Fragment>
            );
          })}
        </nav>
        <div className="sidebar-bottom">
          <div className="system-status">
            <span className="status-dot" />
            身份服务已连接
          </div>
          <div className="profile">
            <div className="avatar">{user.name.slice(0, 1).toUpperCase()}</div>
            <div>
              <b>{user.name}</b>
              <small>{roleLabel[user.role]}</small>
            </div>
            <button
              className="icon-button"
              title="退出登录"
              aria-label="退出登录"
              onClick={() =>
                void run(async () => {
                  await api("/api/auth/logout", "POST");
                  setUser(null);
                })
              }
            >
              <LogOut size={17} />
            </button>
          </div>
        </div>
      </aside>
      {sidebar && (
        <div className="sidebar-backdrop" onClick={() => setSidebar(false)} />
      )}
      <div className="main-area">
        <header className="topbar">
          <div>
            <button
              className="icon-button mobile-toggle"
              onClick={() => setSidebar(!sidebar)}
              aria-label="展开导航"
            >
              <Menu size={20} />
            </button>
            <span className="breadcrumb">工作空间</span>
            <ChevronRight size={14} />
            <b>{labels[page]}</b>
          </div>
          <div>
            <span className="environment">
              <span className="status-dot" />
              运行中
            </span>
            <span className="topbar-divider" />
            <div className="avatar small">
              {user.name.slice(0, 1).toUpperCase()}
            </div>
          </div>
        </header>
        <main className="content">
          <div className="page-heading">
            <div>
              <div className="eyebrow">
                {page === "overview"
                  ? "YOUR IDENTITY, AT A GLANCE"
                  : `WORKSPACE / ${page.toUpperCase()}`}
              </div>
              <h1>
                {page === "overview" ? "身份与访问，一目了然" : labels[page]}
              </h1>
              <p>
                {
                  {
                    overview: `欢迎回来，${user.name}。在这里掌握你的账户安全与应用连接。`,
                    sessions:
                      "查看账户登录的应用与设备，随时结束不再需要的会话。",
                    security: "管理密码和两步验证，为你的账户增加一道保护。",
                    applications: "连接你的业务应用，统一管理注册与登录。",
                    keys: "为应用分配最小必要的 API 权限，安全调用身份服务。",
                    users: "管理账户状态与角色，明确每个人的访问边界。",
                    mail: "连接第三方 SMTP 服务，为用户提供邮件密码找回。",
                    appeals: "核实账户归属后，协助用户安全地找回账户。",
                    audit: "追踪身份、权限和配置变更，保留关键操作记录。",
                    docs: "从创建应用到完成登录，快速接入 Auth API。",
                  }[page]
                }
              </p>
            </div>
            <div className="heading-actions">
              {page === "applications" ? (
                <Button onClick={() => open("application")}>
                  <Plus size={17} />
                  创建应用
                </Button>
              ) : page === "keys" ? (
                <Button onClick={() => open("key")} disabled={!apps.length}>
                  <Plus size={17} />
                  创建 API 密钥
                </Button>
              ) : page === "overview" ? (
                <Button
                  secondary
                  disabled={refreshing}
                  onClick={async () => {
                    setRefreshing(true);
                    try {
                      await refresh();
                      notify("已更新");
                    } catch (e) {
                      setError((e as Error).message);
                    } finally {
                      setRefreshing(false);
                    }
                  }}
                >
                  <Activity size={16} className={refreshing ? "spin" : ""} />
                  刷新状态
                </Button>
              ) : null}
            </div>
          </div>
          {error && !modal && (
            <div className="alert error" role="alert">
              {error}
            </div>
          )}
          {page === "overview" && (
            <>
              <div className="hero-banner">
                <div className="hero-icon">
                  <ShieldCheck size={29} />
                </div>
                <div>
                  <h3>一个身份，安全连接所有应用</h3>
                  <p>从登录会话到 API 访问，让每次连接都在掌握之中。</p>
                </div>
                <button onClick={() => setPage("security")}>
                  检查账户安全
                  <ArrowUpRight size={17} />
                </button>
                <div className="banner-rings" />
              </div>
              <div className="stats-grid">
                <Stat
                  label="当前登录应用"
                  value={new Set(sessions.map((s) => s.app_id)).size}
                  icon={AppWindow}
                  note="包含 Auth 管理控制台"
                />
                <Stat
                  label="在线会话"
                  value={online}
                  icon={Monitor}
                  note="最近 2 分钟内有心跳"
                  green
                />
                <Stat
                  label={privileged ? "有效 API 密钥" : "活跃登录会话"}
                  value={
                    privileged
                      ? keys.filter((k) => !k.revoked).length
                      : sessions.length
                  }
                  icon={KeyRound}
                  note={privileged ? "按应用和权限授权" : "可随时撤销设备访问"}
                />
                <Stat
                  label="两步验证"
                  value={user.tfa ? "已启用" : "待配置"}
                  icon={Fingerprint}
                  note={
                    user.tfa ? "验证器正在保护你的账户" : "建议开启额外身份保护"
                  }
                  green={user.tfa}
                />
              </div>
              <div className="dashboard-grid">
                <section className="panel">
                  <div className="panel-title">
                    <div>
                      <h2>登录中的应用</h2>
                      <p>账户当前的应用访问情况</p>
                    </div>
                    <button
                      className="text-button"
                      onClick={() => setPage("sessions")}
                    >
                      全部会话
                      <ChevronRight size={15} />
                    </button>
                  </div>
                  <div className="app-session-list">
                    {sessions.slice(0, 4).map((s) => (
                      <div className="app-session" key={s.id}>
                        <div
                          className={`app-icon ${s.app_id === "console" ? "blue" : "purple"}`}
                        >
                          {s.app_id === "console" ? (
                            <Shield size={21} />
                          ) : (
                            <AppWindow size={21} />
                          )}
                        </div>
                        <div className="grow">
                          <b>{s.app_name}</b>
                          <small>
                            {s.current ? "当前设备" : s.ip} · {ago(s.last_seen)}
                          </small>
                        </div>
                        <Badge tone={s.online ? "green" : "gray"}>
                          <span className="status-dot" />
                          {s.online ? "在线" : "离线"}
                        </Badge>
                      </div>
                    ))}
                  </div>
                  <div className="panel-foot">
                    <LockKeyhole size={14} />
                    发现陌生设备？前往登录会话撤销访问。
                  </div>
                </section>
                <section className="panel">
                  <div className="panel-title">
                    <div>
                      <h2>安全检查</h2>
                      <p>让账户保持良好的安全状态</p>
                    </div>
                    <ShieldCheck size={21} className="muted" />
                  </div>
                  <SecurityRow
                    done
                    title="账户密码"
                    detail="使用密码保护账户访问"
                    action="修改"
                    onClick={() => open("password")}
                  />
                  <SecurityRow
                    done={user.tfa}
                    title="两步验证"
                    detail={
                      user.tfa
                        ? "已绑定身份验证器"
                        : "增加验证码，进一步保护账户"
                    }
                    action={user.tfa ? "管理" : "去开启"}
                    onClick={() => setPage("security")}
                  />
                  <SecurityRow
                    done={status?.mail_enabled || false}
                    title="账户找回"
                    detail={
                      status?.mail_enabled
                        ? "可通过邮箱重置账户密码"
                        : "通过管理员申诉找回账户"
                    }
                    action={user.role === "admin" ? "配置" : "查看"}
                    onClick={() =>
                      setPage(user.role === "admin" ? "mail" : "security")
                    }
                  />
                </section>
              </div>
              <section className="panel">
                <div className="panel-title">
                  <div>
                    <h2>最近活动</h2>
                    <p>身份与访问的重要动态</p>
                  </div>
                  <button
                    className="text-button"
                    onClick={() => setPage("audit")}
                  >
                    查看日志
                    <ChevronRight size={15} />
                  </button>
                </div>
                <AuditTable audits={audits.slice(0, 5)} />
              </section>
              <div className="content-footer">
                <span>
                  <Shield size={14} />
                  Auth Center
                </span>
                <span>安全连接，从身份开始。</span>
              </div>
            </>
          )}
          {page === "sessions" && (
            <section className="panel">
              <div className="panel-title">
                <h2>
                  全部会话 <span className="count">{sessions.length}</span>
                </h2>
                <Badge tone="green">{online} 个在线</Badge>
              </div>
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>应用 / 设备</th>
                      <th>IP 地址</th>
                      <th>最后活跃</th>
                      <th>状态</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sessions.map((s) => (
                      <tr key={s.id}>
                        <td>
                          <div className="table-app">
                            <div className="app-icon blue">
                              <Monitor size={20} />
                            </div>
                            <div>
                              <b>
                                {s.app_name}{" "}
                                {s.current && <Badge tone="blue">当前</Badge>}
                              </b>
                              <small className="agent" title={s.agent}>
                                {s.agent || "未知客户端"}
                              </small>
                            </div>
                          </div>
                        </td>
                        <td>
                          <code>{s.ip}</code>
                        </td>
                        <td>{ago(s.last_seen)}</td>
                        <td>
                          <Badge tone={s.online ? "green" : "gray"}>
                            {s.online ? "在线" : "离线"}
                          </Badge>
                        </td>
                        <td>
                          <button
                            className="text-button red"
                            onClick={() => open("revokeSession", s)}
                          >
                            结束会话
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="panel-foot">
                <Clock3 size={15} />
                在线状态依据心跳更新。第三方应用需每 30 秒调用一次心跳接口。
              </div>
            </section>
          )}
          {page === "security" && (
            <div className="security-grid">
              <section className="panel security-card">
                <div className="feature-icon blue">
                  <LockKeyhole size={27} />
                </div>
                <h2>账户密码</h2>
                <p>
                  建议使用独立、足够长的密码。修改后，其他设备上的会话会自动退出。
                </p>
                <div className="security-meta">
                  <span>登录邮箱</span>
                  <b>{user.email}</b>
                </div>
                <Button secondary onClick={() => open("password")}>
                  修改密码
                </Button>
              </section>
              <section className="panel security-card">
                <div className="feature-icon purple">
                  <Fingerprint size={27} />
                </div>
                <div className="flex-between">
                  <h2>两步验证（2FA）</h2>
                  <Badge tone={user.tfa ? "green" : "amber"}>
                    {user.tfa ? "已启用" : "未启用"}
                  </Badge>
                </div>
                <p>
                  支持 Google Authenticator、Microsoft Authenticator 等 TOTP
                  验证器，并提供一次性备用恢复码。
                </p>
                <div className="security-meta">
                  <ShieldCheck size={16} />
                  <span>登录时额外验证你的身份</span>
                </div>
                <Button
                  secondary={user.tfa}
                  onClick={() => open(user.tfa ? "disable2fa" : "setup2fa")}
                >
                  {user.tfa ? "关闭两步验证" : "配置身份验证器"}
                  <ArrowUpRight size={16} />
                </Button>
              </section>
              <section className="panel recovery-card">
                <LifeBuoy size={24} />
                <div>
                  <h3>账户找回方式</h3>
                  <p>
                    {status?.mail_enabled
                      ? "邮件服务已启用，你可以在登录页面申请密码重置邮件。"
                      : "当前未配置邮件服务。忘记密码时，请在登录页面向管理员提交申诉。"}{" "}
                    验证器丢失时，可使用备用恢复码，或由管理员核实后处理。
                  </p>
                </div>
              </section>
            </div>
          )}
          {page === "applications" && (
            <>
              {apps.length ? (
                <div className="application-grid">
                  {apps.map((a) => (
                    <section className="panel application-card" key={a.id}>
                      <div className="flex-between">
                        <div className="app-icon large purple">
                          <AppWindow size={25} />
                        </div>
                        <Badge tone="green">已创建</Badge>
                      </div>
                      <h2>{a.name}</h2>
                      <p>{a.description || "尚未填写应用描述"}</p>
                      <div className="app-id">
                        <span>APP ID</span>
                        <code>{a.id}</code>
                        <button
                          className="icon-button"
                          onClick={() => copy(a.id)}
                          aria-label="复制应用 ID"
                        >
                          <Copy size={14} />
                        </button>
                      </div>
                      <div className="application-bottom">
                        <span>
                          <KeyRound size={15} />
                          {a.key_count} 个有效密钥
                        </span>
                        <button
                          className="text-button"
                          onClick={() => {
                            setPage("keys");
                            open("key", a);
                          }}
                        >
                          创建密钥
                          <ArrowUpRight size={15} />
                        </button>
                      </div>
                    </section>
                  ))}
                </div>
              ) : (
                <section className="panel">
                  <Empty
                    title="连接你的第一个应用"
                    description="为业务应用创建身份入口，通过授权的 API Key 接入注册和登录。"
                    action={
                      <Button onClick={() => open("application")}>
                        <Plus size={17} />
                        创建应用
                      </Button>
                    }
                  />
                </section>
              )}
            </>
          )}
          {page === "keys" && (
            <>
              <div className="info-strip">
                <KeyRound size={18} />
                <span>
                  密钥仅在创建时完整显示。请将其保存在应用服务端，并按需授予权限。
                </span>
                <button className="text-button" onClick={() => setPage("docs")}>
                  接入文档
                  <ArrowUpRight size={15} />
                </button>
              </div>
              <section className="panel">
                <TableToolbar
                  title="API 密钥"
                  count={keys.length}
                  search={search}
                  setSearch={setSearch}
                />
                {keys.length ? (
                  <div className="table-scroll">
                    <table>
                      <thead>
                        <tr>
                          <th>密钥名称 / 应用</th>
                          <th>密钥前缀</th>
                          <th>权限范围</th>
                          <th>最近调用</th>
                          <th>状态</th>
                          <th />
                        </tr>
                      </thead>
                      <tbody>
                        {filtered(keys, (k) => k.name + k.app_name).map((k) => (
                          <tr key={k.id}>
                            <td>
                              <b>{k.name}</b>
                              <small>{k.app_name}</small>
                            </td>
                            <td>
                              <code>{k.prefix}••••••</code>
                            </td>
                            <td>
                              <div className="scope-list">
                                {k.scopes.map((v) => (
                                  <Badge key={v}>{v}</Badge>
                                ))}
                              </div>
                            </td>
                            <td>{time(k.last_used)}</td>
                            <td>
                              <Badge tone={k.revoked ? "gray" : "green"}>
                                {k.revoked ? "已撤销" : "有效"}
                              </Badge>
                            </td>
                            <td>
                              {!k.revoked && (
                                <button
                                  className="text-button red"
                                  onClick={() => open("revokeKey", k)}
                                >
                                  撤销
                                </button>
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    {!filtered(keys, (k) => k.name + k.app_name).length && (
                      <div className="no-results">没有匹配的密钥</div>
                    )}
                  </div>
                ) : (
                  <Empty
                    icon={KeyRound}
                    title="还没有 API 密钥"
                    description={
                      apps.length
                        ? "为应用创建一个带权限的密钥，开始调用身份服务。"
                        : "先创建一个应用，再为它分配 API 密钥。"
                    }
                    action={
                      <Button
                        onClick={() =>
                          apps.length ? open("key") : setPage("applications")
                        }
                      >
                        {apps.length ? "创建密钥" : "前往应用管理"}
                        <ArrowUpRight size={16} />
                      </Button>
                    }
                  />
                )}
              </section>
            </>
          )}
          {page === "users" && (
            <section className="panel">
              <TableToolbar
                title="用户账户"
                count={users.length}
                search={search}
                setSearch={setSearch}
              />
              <div className="table-scroll">
                <table>
                  <thead>
                    <tr>
                      <th>用户</th>
                      <th>身份角色</th>
                      <th>两步验证</th>
                      <th>账户状态</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {filtered(users, (u) => u.name + u.email).map((u) => (
                      <tr key={u.id}>
                        <td>
                          <div className="table-app">
                            <div className="avatar">{u.name.slice(0, 1)}</div>
                            <div>
                              <b>
                                {u.name}{" "}
                                {u.id === user.id && (
                                  <Badge tone="blue">我</Badge>
                                )}
                              </b>
                              <small>{u.email}</small>
                            </div>
                          </div>
                        </td>
                        <td>
                          <Badge
                            tone={
                              u.role === "admin"
                                ? "purple"
                                : u.role === "developer"
                                  ? "blue"
                                  : "gray"
                            }
                          >
                            {roleLabel[u.role]}
                          </Badge>
                        </td>
                        <td>{u.tfa ? "已启用" : "未启用"}</td>
                        <td>
                          <Badge tone={u.disabled ? "gray" : "green"}>
                            {u.disabled ? "已停用" : "正常"}
                          </Badge>
                        </td>
                        <td>
                          <button
                            className="text-button"
                            onClick={() => open("user", u)}
                          >
                            管理权限
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {!filtered(users, (u) => u.name + u.email).length && (
                  <div className="no-results">没有匹配的账户</div>
                )}
              </div>
              <div className="panel-foot">
                普通用户通过已授权的第三方 API 注册。开发者可创建自己的应用及
                API 密钥。
              </div>
            </section>
          )}
          {page === "mail" && mail && (
            <div className="mail-grid">
              <section className="panel">
                <div className="panel-title">
                  <div>
                    <h2>SMTP 邮件配置</h2>
                    <p>支持企业邮箱、QQ 邮箱、Gmail 等第三方邮件服务</p>
                  </div>
                  <Badge tone={mail.enabled ? "green" : "gray"}>
                    {mail.enabled ? "已启用" : "未启用"}
                  </Badge>
                </div>
                <form
                  className="panel-form"
                  key={JSON.stringify(mail)}
                  onSubmit={submit(async (f) => {
                    await api("/api/admin/mail", "PUT", {
                      enabled: f.enabled === "on",
                      host: f.host,
                      port: Number(f.port),
                      username: f.username,
                      password: f.password,
                      from: f.from,
                      tls: f.tls,
                    });
                    await refresh();
                    setStatus(await api("/api/status"));
                    notify("邮件配置已保存");
                  })}
                >
                  <label className="switch-field">
                    <div>
                      <b>启用邮件找回</b>
                      <small>关闭时，用户只能向管理员申诉找回</small>
                    </div>
                    <input
                      type="checkbox"
                      name="enabled"
                      defaultChecked={mail.enabled}
                    />
                  </label>
                  <div className="form-row">
                    <Field label="SMTP 主机">
                      <input
                        name="host"
                        placeholder="smtp.example.com"
                        defaultValue={mail.host}
                      />
                    </Field>
                    <Field label="端口">
                      <input
                        name="port"
                        type="number"
                        min={1}
                        max={65535}
                        defaultValue={mail.port}
                      />
                    </Field>
                  </div>
                  <Field label="传输加密">
                    <select name="tls" defaultValue={mail.tls}>
                      <option value="starttls">
                        STARTTLS（通常使用 587 端口）
                      </option>
                      <option value="tls">
                        SSL / TLS（通常使用 465 端口）
                      </option>
                    </select>
                  </Field>
                  <Field label="SMTP 用户名">
                    <input
                      name="username"
                      placeholder="service@example.com"
                      defaultValue={mail.username}
                      autoComplete="off"
                    />
                  </Field>
                  <Field
                    label="SMTP 密码 / 授权码"
                    hint={
                      mail.has_password
                        ? "已保存密码；留空将保留现有密码"
                        : "邮箱服务商可能要求使用专用授权码"
                    }
                  >
                    <input
                      name="password"
                      type="password"
                      placeholder={
                        mail.has_password
                          ? "••••••••（已配置）"
                          : "输入邮箱密码或应用授权码"
                      }
                      autoComplete="new-password"
                    />
                  </Field>
                  <Field label="发件邮箱">
                    <input
                      name="from"
                      type="email"
                      placeholder="noreply@example.com"
                      defaultValue={mail.from}
                    />
                  </Field>
                  <div className="form-actions">
                    <Button type="submit" disabled={busy}>
                      {busy ? (
                        <LoaderCircle size={16} className="spin" />
                      ) : (
                        <Check size={16} />
                      )}
                      保存配置
                    </Button>
                    <Button
                      type="button"
                      secondary
                      disabled={busy || !mail.enabled}
                      onClick={() =>
                        void run(async () => {
                          await api("/api/admin/mail/test", "POST");
                          notify(`测试邮件已发送至 ${user.email}`);
                        })
                      }
                    >
                      发送测试邮件
                    </Button>
                  </div>
                </form>
              </section>
              <aside className="panel mail-help">
                <div className="feature-icon blue">
                  <Mail size={25} />
                </div>
                <h3>让账户找回更顺畅</h3>
                <p>启用后，用户可在登录页通过邮箱获取一次性密码重置链接。</p>
                <div className="help-step">
                  <span>1</span>
                  <div>
                    <b>填写服务配置</b>
                    <p>在邮箱服务商处开启 SMTP，获取专用授权码。</p>
                  </div>
                </div>
                <div className="help-step">
                  <span>2</span>
                  <div>
                    <b>测试邮件发送</b>
                    <p>保存配置后，向当前管理员邮箱发送测试邮件。</p>
                  </div>
                </div>
                <div className="help-step">
                  <span>3</span>
                  <div>
                    <b>随时处理申诉</b>
                    <p>无法访问邮箱的用户仍可提交管理员申诉。</p>
                  </div>
                </div>
                <div className="help-note">
                  <LockKeyhole size={16} />
                  SMTP 凭据经过加密保存
                </div>
              </aside>
            </div>
          )}
          {page === "appeals" && (
            <section className="panel">
              <div className="panel-title">
                <h2>账户找回申诉</h2>
                <Badge tone="amber">{pending} 项待处理</Badge>
              </div>
              {appeals.length ? (
                <div className="appeal-list">
                  {appeals.map((a) => (
                    <div className="appeal-item" key={a.id}>
                      <div className="flex-between">
                        <h3>{a.email}</h3>
                        <Badge
                          tone={
                            a.status === "pending"
                              ? "amber"
                              : a.status === "approved"
                                ? "green"
                                : "gray"
                          }
                        >
                          {a.status === "pending"
                            ? "待核实"
                            : a.status === "approved"
                              ? "已批准"
                              : "已拒绝"}
                        </Badge>
                      </div>
                      <p>{a.reason}</p>
                      <div className="appeal-meta">
                        <span>联系方式：{a.contact}</span>
                        <span>{time(a.created_at)}</span>
                      </div>
                      {a.note && (
                        <div className="appeal-note">处理说明：{a.note}</div>
                      )}
                      {a.status === "pending" && (
                        <Button secondary onClick={() => open("appeal", a)}>
                          核实并处理
                          <ChevronRight size={16} />
                        </Button>
                      )}
                    </div>
                  ))}
                </div>
              ) : (
                <Empty
                  icon={LifeBuoy}
                  title="暂无找回申诉"
                  description="用户提交的账户申诉会显示在这里。请核实账户归属后再提供找回链接。"
                />
              )}
            </section>
          )}
          {page === "audit" && (
            <section className="panel">
              <TableToolbar
                title="操作记录"
                count={audits.length}
                search={search}
                setSearch={setSearch}
              />
              <AuditTable
                audits={filtered(
                  audits,
                  (a) => a.email + a.action + (actionLabel[a.action] || ""),
                )}
              />
              <div className="panel-foot">
                展示最近 100 条记录。
                {user.role === "admin"
                  ? "管理员可查看所有用户操作。"
                  : "仅展示你自己的操作。"}
              </div>
            </section>
          )}
          {page === "docs" && <Docs copy={copy} />}
        </main>
      </div>
      {modal && (
        <ModalShell
          title={
            {
              application: "创建应用",
              key: "创建 API 密钥",
              secret: "请保存你的密钥",
              password: "修改账户密码",
              setup2fa: "配置两步验证",
              verify2fa: "绑定身份验证器",
              backup: "保存备用恢复码",
              disable2fa: "关闭两步验证",
              revokeSession: "结束登录会话",
              revokeKey: "撤销 API 密钥",
              user: "管理用户权限",
              appeal: "处理账户申诉",
              recovery: "账户找回链接",
            }[modal.type] || ""
          }
          close={close}
        >
          {error && (
            <div className="alert error" role="alert">
              {error}
            </div>
          )}
          {modal.type === "application" && (
            <form
              onSubmit={submit(async (f) => {
                await api("/api/applications", "POST", f);
                await refresh();
                setModal(null);
                notify("应用已创建");
              })}
            >
              <Field label="应用名称">
                <input
                  name="name"
                  placeholder="例如：客户服务平台"
                  required
                  maxLength={80}
                />
              </Field>
              <Field label="应用描述">
                <textarea
                  name="description"
                  placeholder="描述应用的用途"
                  maxLength={500}
                  rows={3}
                />
              </Field>
              <div className="form-actions">
                <Button disabled={busy}>创建应用</Button>
              </div>
            </form>
          )}
          {modal.type === "key" && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const data = new FormData(e.currentTarget);
                void run(async () => {
                  const b = await api("/api/keys", "POST", {
                    app_id: data.get("app_id"),
                    name: data.get("name"),
                    scopes: data.getAll("scopes"),
                  });
                  await refresh();
                  setModal({ type: "secret", data: b.key });
                });
              }}
            >
              <Field label="所属应用">
                <select
                  name="app_id"
                  defaultValue={modal.data?.id || apps[0]?.id}
                  required
                >
                  {apps.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="密钥名称">
                <input
                  name="name"
                  placeholder="例如：生产环境服务端"
                  required
                  maxLength={80}
                />
              </Field>
              <div className="field">
                <span>授权范围</span>
                {[
                  ["auth:register", "注册账户", "允许应用创建普通用户账户"],
                  ["auth:login", "账户登录", "验证密码和两步验证码，创建会话"],
                  [
                    "session:read",
                    "读取与维护会话",
                    "获取当前用户并更新在线心跳",
                  ],
                ].map(([scope, label, desc]) => (
                  <label className="scope-option" key={scope}>
                    <input
                      type="checkbox"
                      name="scopes"
                      value={scope}
                      defaultChecked
                    />
                    <span>
                      <b>
                        {label} <code>{scope}</code>
                      </b>
                      <small>{desc}</small>
                    </span>
                  </label>
                ))}
              </div>
              <Button disabled={busy}>生成 API 密钥</Button>
            </form>
          )}
          {(modal.type === "secret" || modal.type === "recovery") && (
            <>
              <div className="alert info">
                {modal.type === "secret"
                  ? "完整密钥只显示这一次，请妥善保存。"
                  : "此链接 30 分钟内有效。请通过已核实的联系方式交给申诉人，仅显示一次。"}
              </div>
              <div className="secret-value">
                <code>{modal.data}</code>
              </div>
              <Button onClick={() => copy(modal.data)}>
                <Copy size={16} />
                复制{modal.type === "secret" ? "密钥" : "链接"}
              </Button>
            </>
          )}
          {modal.type === "password" && (
            <form
              onSubmit={submit(async (f) => {
                await api("/api/password", "POST", f);
                setModal(null);
                notify("密码已更新，其他设备会话已撤销");
              })}
            >
              <Field label="当前密码">
                <input
                  type="password"
                  name="current_password"
                  autoComplete="current-password"
                  required
                />
              </Field>
              <Field label="新密码" hint="10–72 个字节">
                <input
                  type="password"
                  name="password"
                  minLength={10}
                  maxLength={72}
                  autoComplete="new-password"
                  required
                />
              </Field>
              {user.tfa && (
                <Field label="两步验证码 / 备用恢复码">
                  <input name="code" autoComplete="one-time-code" required />
                </Field>
              )}
              <Button disabled={busy}>更新密码</Button>
            </form>
          )}
          {modal.type === "setup2fa" && (
            <form
              onSubmit={submit(async (f) => {
                const b = await api("/api/2fa/setup", "POST", f);
                setModal({ type: "verify2fa", data: b });
              })}
            >
              <p className="modal-description">
                先验证账户密码，然后使用验证器扫描二维码。
              </p>
              <Field label="当前密码">
                <input
                  type="password"
                  name="password"
                  required
                  autoComplete="current-password"
                />
              </Field>
              <Button disabled={busy}>
                继续
                <ChevronRight size={16} />
              </Button>
            </form>
          )}
          {modal.type === "verify2fa" && (
            <>
              <p className="modal-description">
                在身份验证器中扫描二维码，输入生成的 6 位验证码完成绑定。
              </p>
              <div className="qr">
                <QRCodeSVG value={modal.data.url} size={176} marginSize={2} />
              </div>
              <Field label="也可手动输入密钥">
                <div className="inline-input">
                  <code>{modal.data.secret}</code>
                  <button
                    className="icon-button"
                    onClick={() => copy(modal.data.secret)}
                    aria-label="复制验证器密钥"
                  >
                    <Copy size={16} />
                  </button>
                </div>
              </Field>
              <form
                onSubmit={submit(async (f) => {
                  const b = await api("/api/2fa/enable", "POST", f);
                  await refresh();
                  setModal({ type: "backup", data: b.backup_codes });
                })}
              >
                <Field label="验证器验证码">
                  <input
                    name="code"
                    placeholder="000000"
                    pattern="[0-9]{6}"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    required
                  />
                </Field>
                <Button disabled={busy}>验证并开启</Button>
              </form>
            </>
          )}
          {modal.type === "backup" && (
            <>
              <div className="alert info">
                每个备用码只能使用一次。验证器不可用时可用它登录，请立即离线保存。
              </div>
              <div className="backup-codes">
                {modal.data.map((c: string) => (
                  <code key={c}>{c}</code>
                ))}
              </div>
              <Button onClick={() => copy(modal.data.join("\n"))}>
                <Copy size={16} />
                复制全部备用码
              </Button>
            </>
          )}
          {modal.type === "disable2fa" && (
            <form
              onSubmit={submit(async (f) => {
                await api("/api/2fa/disable", "POST", f);
                await refresh();
                setModal(null);
                notify("两步验证已关闭");
              })}
            >
              <div className="alert warning">
                关闭后，登录账户只需要邮箱和密码。
              </div>
              <Field label="账户密码">
                <input
                  type="password"
                  name="password"
                  required
                  autoComplete="current-password"
                />
              </Field>
              <Field label="验证码或备用恢复码">
                <input name="code" required autoComplete="one-time-code" />
              </Field>
              <Button danger disabled={busy}>
                关闭两步验证
              </Button>
            </form>
          )}
          {(modal.type === "revokeKey" || modal.type === "revokeSession") && (
            <>
              <p className="modal-description">
                {modal.type === "revokeKey"
                  ? `撤销「${modal.data.name}」后，使用此密钥的 API 调用将立即失效。`
                  : `确定结束「${modal.data.app_name}」的登录会话？该设备需要重新登录。`}
              </p>
              <div className="form-actions">
                <Button
                  danger
                  disabled={busy}
                  onClick={() =>
                    void run(async () => {
                      if (modal.type === "revokeKey") {
                        await api(`/api/keys/${modal.data.id}`, "DELETE");
                        await refresh();
                      } else {
                        await api(`/api/sessions/${modal.data.id}`, "DELETE");
                        if (modal.data.current) {
                          setUser(null);
                        } else {
                          await refresh();
                        }
                      }
                      setModal(null);
                      notify("访问已撤销");
                    })
                  }
                >
                  确认{modal.type === "revokeKey" ? "撤销" : "结束"}
                </Button>
                <Button secondary onClick={close} disabled={busy}>
                  取消
                </Button>
              </div>
            </>
          )}
          {modal.type === "user" && (
            <form
              onSubmit={submit(async (f) => {
                await api(`/api/admin/users/${modal.data.id}`, "PATCH", {
                  role: f.role,
                  disabled: f.disabled === "on",
                });
                await refresh();
                setModal(null);
                notify("用户权限已更新");
              })}
            >
              <p className="modal-description">{modal.data.email}</p>
              <Field label="身份角色">
                <select name="role" defaultValue={modal.data.role}>
                  <option value="user">普通用户 — 管理自己的账户与会话</option>
                  <option value="developer">
                    开发者 — 创建应用并管理自己的 API Key
                  </option>
                  <option value="admin">管理员 — 管理全部用户与系统配置</option>
                </select>
              </Field>
              <label className="scope-option">
                <input
                  type="checkbox"
                  name="disabled"
                  defaultChecked={modal.data.disabled}
                />
                <span>
                  <b>停用账户</b>
                  <small>撤销全部登录会话，并停用其应用 API Key</small>
                </span>
              </label>
              <div className="form-actions">
                <Button disabled={busy}>保存权限</Button>
              </div>
            </form>
          )}
          {modal.type === "appeal" && (
            <form
              onSubmit={submit(async (f) => {
                const b = await api(
                  `/api/admin/appeals/${modal.data.id}/resolve`,
                  "POST",
                  {
                    approve: f.action === "approve",
                    note: f.note,
                    reset_2fa: f.reset_2fa === "on",
                  },
                );
                if (b.recovery_url) {
                  setModal({ type: "recovery", data: b.recovery_url });
                  await refresh();
                } else {
                  await refresh();
                  setModal(null);
                  notify("申诉已拒绝");
                }
              })}
            >
              <div className="alert warning">
                请先通过联系信息核实账户归属，再批准申诉。
              </div>
              <p className="modal-description">
                账户：{modal.data.email}
                <br />
                联系方式：{modal.data.contact}
              </p>
              <Field label="处理结果">
                <select name="action">
                  <option value="approve">批准 — 生成一次性找回链接</option>
                  <option value="reject">拒绝申诉</option>
                </select>
              </Field>
              <Field label="核实与处理说明">
                <textarea
                  name="note"
                  required
                  maxLength={2000}
                  rows={3}
                  placeholder="记录身份核实方式与处理依据"
                />
              </Field>
              <label className="scope-option">
                <input type="checkbox" name="reset_2fa" />
                <span>
                  <b>同时重置两步验证</b>
                  <small>仅在已核实验证器丢失时勾选，原备用码也将失效</small>
                </span>
              </label>
              <Button disabled={busy}>确认处理</Button>
            </form>
          )}
        </ModalShell>
      )}
      {toast && (
        <div className="toast" role="status">
          <Check size={18} />
          {toast}
        </div>
      )}
    </div>
  );
}
function Stat({
  label,
  value,
  icon: Icon,
  note,
  green = false,
}: {
  label: string;
  value: string | number;
  icon: React.ElementType;
  note: string;
  green?: boolean;
}) {
  return (
    <div className="stat panel">
      <div className="stat-top">
        <span>{label}</span>
        <Icon size={18} />
      </div>
      <div className="stat-value">
        {value}
        {typeof value === "number" && <span>个</span>}
      </div>
      <small>
        <span className={`tiny-dot ${green ? "green" : ""}`} />
        {note}
      </small>
    </div>
  );
}
function SecurityRow({
  done,
  title,
  detail,
  action,
  onClick,
}: {
  done: boolean;
  title: string;
  detail: string;
  action: string;
  onClick: () => void;
}) {
  return (
    <div className="security-row">
      <div className={`check-circle ${done ? "done" : ""}`}>
        {done ? <Check size={14} /> : <LockKeyhole size={13} />}
      </div>
      <div className="grow">
        <b>{title}</b>
        <small>{detail}</small>
      </div>
      <button className="text-button" onClick={onClick}>
        {action}
        <ChevronRight size={14} />
      </button>
    </div>
  );
}
function TableToolbar({
  title,
  count,
  search,
  setSearch,
}: {
  title: string;
  count: number;
  search: string;
  setSearch: (s: string) => void;
}) {
  return (
    <div className="panel-title">
      <h2>
        {title} <span className="count">{count}</span>
      </h2>
      <div className="search-input">
        <Search size={16} />
        <input
          aria-label="搜索"
          placeholder="搜索…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>
    </div>
  );
}
function AuditTable({ audits }: { audits: Audit[] }) {
  return audits.length ? (
    <div className="table-scroll">
      <table>
        <thead>
          <tr>
            <th>操作事件</th>
            <th>操作账户</th>
            <th>IP 地址</th>
            <th>时间</th>
          </tr>
        </thead>
        <tbody>
          {audits.map((a, i) => (
            <tr key={i}>
              <td>
                <span className="event-label">
                  <span
                    className={`event-dot ${a.action.includes("revoke") || a.action.includes("failed") ? "amber" : ""}`}
                  />
                  {actionLabel[a.action] || a.action}
                </span>
                {a.detail && (
                  <small className="audit-detail" title={a.detail}>
                    {a.detail}
                  </small>
                )}
              </td>
              <td>{a.email}</td>
              <td>
                <code>{a.ip}</code>
              </td>
              <td>{time(a.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  ) : (
    <Empty
      icon={ScrollText}
      title="暂无操作记录"
      description="账户登录、配置变更等操作会记录在这里。"
    />
  );
}
function Docs({ copy }: { copy: (s: string) => void }) {
  const snippet = `# 在应用服务端调用，禁止在浏览器暴露 API Key\ncurl -X POST '${location.origin}/v1/auth/login' \\\n  -H 'Content-Type: application/json' \\\n  -H 'X-API-Key: YOUR_API_KEY' \\\n  -d '{"email":"user@example.com","password":"your-password","code":"123456"}'`;
  return (
    <div className="docs">
      <section className="panel docs-intro">
        <div className="feature-icon blue">
          <Terminal size={25} />
        </div>
        <h2>开始连接你的应用</h2>
        <p>
          管理员授权开发者角色 → 创建应用 → 创建 API Key →
          在应用服务端调用接口。
        </p>
        <div className="info-strip">
          所有账户共用一个身份目录。第三方注册创建普通用户，不会授予管理权限。
        </div>
      </section>
      <section className="panel">
        <div className="panel-title">
          <h2>API 端点</h2>
          <Badge tone="blue">v1</Badge>
        </div>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>方法 / 路径</th>
                <th>所需权限</th>
                <th>说明</th>
              </tr>
            </thead>
            <tbody>
              {[
                [
                  "POST",
                  "/v1/auth/register",
                  "auth:register",
                  "注册：email、name、password",
                ],
                [
                  "POST",
                  "/v1/auth/login",
                  "auth:login",
                  "登录：email、password、code（启用 2FA 时必填）",
                ],
                [
                  "GET",
                  "/v1/session",
                  "session:read",
                  "获取登录用户，需要 Bearer Token",
                ],
                [
                  "POST",
                  "/v1/session/heartbeat",
                  "session:read",
                  "每 30 秒上报心跳，需要 Bearer Token",
                ],
                [
                  "POST",
                  "/v1/auth/logout",
                  "任意有效 Key",
                  "撤销当前会话，需要 Bearer Token",
                ],
              ].map(([method, path, scope, desc]) => (
                <tr key={path}>
                  <td>
                    <Badge tone={method === "GET" ? "green" : "blue"}>
                      {method}
                    </Badge>{" "}
                    <code>{path}</code>
                  </td>
                  <td>
                    <code>{scope}</code>
                  </td>
                  <td>{desc}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      <section className="panel code-panel">
        <div className="panel-title">
          <h2>登录调用示例</h2>
          <button className="text-button" onClick={() => copy(snippet)}>
            <Copy size={15} />
            复制
          </button>
        </div>
        <pre>
          <code>{snippet}</code>
        </pre>
        <div className="panel-foot">
          登录响应包含 access_token，有效期 24 小时。会话接口需同时携带
          X-API-Key 和 Authorization: Bearer
          &lt;access_token&gt;，且必须属于同一应用。
        </div>
      </section>
      <section className="panel docs-intro">
        <h3>处理两步验证与错误</h3>
        <p>
          未提交验证码时，启用 2FA 的账户会收到 401 与 requires_2fa:
          true。提示用户输入验证码或备用恢复码后，再次提交登录请求。验证码不可重复使用。
        </p>
        <p>
          400：请求不合法 · 401：认证失败 · 403：权限不足 · 409：账户冲突 ·
          429：请求频繁（每 IP 每分钟最多 30 次认证请求）。
        </p>
      </section>
    </div>
  );
}
createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
