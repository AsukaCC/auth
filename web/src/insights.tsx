import React, { useEffect, useRef, useState } from "react";
import { actionLabel } from "./audit";
import {
  ArrowLeft,
  ChevronLeft,
  ChevronRight,
  RefreshCw,
  Search,
  UserRound,
  Users,
  Activity,
  Fingerprint,
  LogIn,
  Building2,
  LoaderCircle,
} from "lucide-react";

type API = (path: string, method?: string, data?: unknown) => Promise<any>;
type Account = {
  id: string;
  name: string;
  email: string;
  role: string;
  disabled: boolean;
  tfa: boolean;
  created_at: number;
};
type Profile = {
  name: string;
  phone: string;
  organization: string;
  department: string;
  job_title: string;
  location: string;
  bio: string;
  locale: string;
  time_zone: string;
  revision: number;
  updated_at: number;
};
type Stats = {
  generated_at: number;
  timezone: string;
  summary: {
    total_users: number;
    enabled_users: number;
    disabled_users: number;
    tfa_users: number;
    online_users: number;
    active_today: number;
    active_7d: number;
    active_30d: number;
    logins_30d: number;
    new_users_30d: number;
    last_seen: number;
  };
  days: { date: string; active: number; logins: number; new_users: number }[];
  apps: { id: string; name: string; active_users: number; logins: number }[];
  roles: { name: string; count: number }[];
};
type Detail = {
  user: Account;
  profile: Profile;
  annotations: { tags: string[]; notes: string; revision: number };
  analytics: Stats;
  sessions: {
    id: string;
    app_name: string;
    ip: string;
    agent: string;
    last_seen: number;
    online: boolean;
  }[];
  audits: { action: string; detail: string; created_at: number; ip: string }[];
};
type Directory = {
  items: {
    user: Account;
    organization: string;
    department: string;
    tags: string[];
    last_seen: number;
    online: boolean;
  }[];
  total: number;
  page: number;
  page_size: number;
};
const roles: Record<string, string> = {
  admin: "管理员",
  developer: "开发者",
  user: "普通用户",
};
const date = (value: number) =>
  value ? new Date(value * 1000).toLocaleString("zh-CN") : "暂无记录";

function useResource<T>(path: string, api: API, revision = 0) {
  const [state, setState] = useState<{
    data: T | null;
    error: string;
    loading: boolean;
  }>({ data: null, error: "", loading: true });
  useEffect(() => {
    let current = true;
    setState({ data: null, error: "", loading: true });
    api(path)
      .then((data) => {
        if (current) setState({ data, error: "", loading: false });
      })
      .catch((error) => {
        if (current)
          setState({ data: null, error: error.message, loading: false });
      });
    return () => {
      current = false;
    };
  }, [path, api, revision]);
  return state;
}
function ResourceStatus({
  loading,
  error,
}: {
  loading: boolean;
  error: string;
}) {
  if (error)
    return (
      <div className="alert error" role="alert">
        {error}
      </div>
    );
  return loading ? (
    <div className="insight-loading" role="status">
      <LoaderCircle className="spin" size={18} />
      正在读取数据…
    </div>
  ) : null;
}
function InputField({
  label,
  name,
  value,
  limit,
  required = false,
  hint,
}: {
  label: string;
  name: string;
  value: string;
  limit: number;
  required?: boolean;
  hint?: string;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      <input
        name={name}
        defaultValue={value}
        maxLength={limit}
        required={required}
      />
      {hint && <small>{hint}</small>}
    </label>
  );
}

export function ProfileEditor({
  api,
  onSaved,
}: {
  api: API;
  onSaved: () => Promise<void>;
}) {
  const [revision, setRevision] = useState(0);
  const state = useResource<Profile>("/api/profile", api, revision);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false);
  return (
    <div className="profile-layout">
      <section className="panel">
        <div className="panel-title">
          <div>
            <h2>个人资料</h2>
            <p>完善工作信息，让你的身份更清晰</p>
          </div>
          <UserRound size={21} />
        </div>
        <ResourceStatus {...state} />
        {state.data && (
          <form
            className="panel-form"
            key={`${revision}-${state.data.revision}`}
            onSubmit={async (event) => {
              event.preventDefault();
              if (busy) return;
              const values = Object.fromEntries(
                new FormData(event.currentTarget),
              );
              setBusy(true);
              setError("");
              setSaved(false);
              try {
                await api("/api/profile", "PUT", {
                  ...values,
                  expected_revision: state.data!.revision,
                });
                setRevision((value) => value + 1);
                await onSaved();
                setSaved(true);
              } catch (err) {
                setError((err as Error).message);
              } finally {
                setBusy(false);
              }
            }}
          >
            {error && (
              <div className="alert error" role="alert">
                {error}
                <button
                  type="button"
                  className="text-button"
                  onClick={() => {
                    setError("");
                    setRevision((value) => value + 1);
                  }}
                >
                  重新加载
                </button>
              </div>
            )}
            {saved && (
              <div className="alert info" role="status">
                个人资料已保存
              </div>
            )}
            <InputField
              label="姓名 / 显示名称"
              name="name"
              value={state.data.name}
              limit={80}
              required
            />
            <div className="profile-fields">
              <InputField
                label="组织 / 公司"
                name="organization"
                value={state.data.organization}
                limit={160}
              />
              <InputField
                label="部门"
                name="department"
                value={state.data.department}
                limit={120}
              />
              <InputField
                label="职位"
                name="job_title"
                value={state.data.job_title}
                limit={120}
              />
              <InputField
                label="所在地区"
                name="location"
                value={state.data.location}
                limit={160}
              />
            </div>
            <InputField
              label="联系电话（可选）"
              name="phone"
              value={state.data.phone}
              limit={32}
              hint="仅作为资料保存，未验证，不可用于登录或找回账户。"
            />
            <label className="field">
              <span>个人简介</span>
              <textarea
                name="bio"
                defaultValue={state.data.bio}
                maxLength={1000}
                rows={3}
              />
            </label>
            <div className="profile-fields">
              <InputField
                label="语言偏好"
                name="locale"
                value={state.data.locale}
                limit={35}
                hint="例如 zh-CN、en-US；当前界面使用简体中文。"
              />
              <InputField
                label="时区偏好"
                name="time_zone"
                value={state.data.time_zone}
                limit={80}
                hint="例如 Asia/Shanghai；活跃统计统一使用 UTC。"
              />
            </div>
            <div className="form-actions">
              <button className="button" disabled={busy}>
                {busy && <LoaderCircle size={15} className="spin" />}保存资料
              </button>
              <button
                type="button"
                className="button secondary"
                disabled={busy}
                onClick={() => {
                  setError("");
                  setSaved(false);
                  setRevision((value) => value + 1);
                }}
              >
                重新加载
              </button>
            </div>
          </form>
        )}
      </section>
      <aside className="panel profile-help">
        <div className="feature-icon blue">
          <Fingerprint size={25} />
        </div>
        <h2>你的资料，你来定义</h2>
        <p>
          组织、部门与职位帮助管理员了解用户构成。这里填写的信息均为自述资料，不代表经过身份验证。
        </p>
        <p>
          联系电话不参与认证。邮箱、角色与安全设置沿用独立的安全流程；管理员内部标签和备注不会提供给第三方应用。
        </p>
        <p>所有字段长度以 UTF-8 字节计算；中文通常占 3 字节。</p>
      </aside>
    </div>
  );
}

function Trend({ stats }: { stats: Stats }) {
  const [metric, setMetric] = useState<"active" | "logins" | "new_users">(
    "active",
  );
  const [selected, setSelected] = useState<number | null>(null);
  const names = {
    active: "活跃用户",
    logins: "登录次数",
    new_users: "新增用户",
  };
  const max = Math.max(1, ...stats.days.map((day) => day[metric]));
  const height = 150,
    width = 700;
  const points = stats.days
    .map(
      (day, i) =>
        `${28 + (i * (width - 56)) / Math.max(1, stats.days.length - 1)},${height + 15 - (day[metric] / max) * height}`,
    )
    .join(" ");
  const selectedDay = stats.days[selected ?? stats.days.length - 1];
  return (
    <section className="panel trend-panel">
      <div className="panel-title">
        <div>
          <h2>最近 30 天的使用趋势</h2>
          <p>按 UTC 自然日统计 · 活跃指成功登录或有效心跳</p>
        </div>
        <Activity size={20} />
      </div>
      <div className="trend-controls">
        <div className="segmented" role="group" aria-label="趋势指标">
          {(Object.keys(names) as (keyof typeof names)[]).map((key) => (
            <button
              key={key}
              aria-pressed={metric === key}
              onClick={() => setMetric(key)}
            >
              {names[key]}
            </button>
          ))}
        </div>
        <span aria-live="polite">
          {selectedDay?.date} · {names[metric]}{" "}
          <b>{selectedDay?.[metric] ?? 0}</b>
        </span>
      </div>
      <div className="trend-chart">
        <svg
          viewBox={`0 0 ${width} 195`}
          role="img"
          aria-label={`最近30天${names[metric]}趋势，最高 ${max}`}
        >
          {[0, 0.5, 1].map((value) => (
            <g key={value}>
              <line
                x1="28"
                x2={width - 28}
                y1={15 + height * value}
                y2={15 + height * value}
                stroke="currentColor"
                strokeOpacity="0.09"
              />
              <text x="1" y={19 + height * value}>
                {Number((max * (1 - value)).toFixed(1))}
              </text>
            </g>
          ))}
          <polygon
            points={`28,165 ${points} ${width - 28},165`}
            fill="var(--blue)"
            fillOpacity="0.09"
          />
          <polyline
            points={points}
            fill="none"
            stroke="var(--blue)"
            strokeWidth="2.5"
            strokeLinejoin="round"
          />
          {stats.days.map((day, index) => (
            <g key={day.date}>
              <circle
                cx={28 + (index * (width - 56)) / 29}
                cy={165 - (day[metric] / max) * height}
                r={selected === index ? 5 : 2.5}
                fill="var(--blue)"
              />
              <rect
                x={17 + (index * (width - 56)) / 29}
                y="10"
                width="22"
                height="165"
                fill="transparent"
                onMouseEnter={() => setSelected(index)}
                onClick={() => setSelected(index)}
              >
                <title>
                  {day.date}：{names[metric]} {day[metric]}
                </title>
              </rect>
            </g>
          ))}
          <text x="28" y="188">
            {stats.days[0]?.date.slice(5)}
          </text>
          <text x={width - 60} y="188">
            {stats.days.at(-1)?.date.slice(5)}
          </text>
        </svg>
      </div>
      <details className="chart-data">
        <summary>查看每日数据表</summary>
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>日期（UTC）</th>
                <th>活跃用户</th>
                <th>登录次数</th>
                <th>新增用户</th>
              </tr>
            </thead>
            <tbody>
              {stats.days.map((day) => (
                <tr key={day.date}>
                  <td>{day.date}</td>
                  <td>{day.active}</td>
                  <td>{day.logins}</td>
                  <td>{day.new_users}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </section>
  );
}
function StatCard({
  label,
  value,
  note,
  icon: Icon,
}: {
  label: string;
  value: number;
  note: string;
  icon: React.ElementType;
}) {
  return (
    <div className="stat panel">
      <div className="stat-top">
        <span>{label}</span>
        <Icon size={18} />
      </div>
      <div className="stat-value">{value.toLocaleString("zh-CN")}</div>
      <small>{note}</small>
    </div>
  );
}
function Breakdown({ stats }: { stats: Stats }) {
  return (
    <div className="insights-breakdown">
      <section className="panel">
        <div className="panel-title">
          <h2>应用使用分布</h2>
          <span className="muted">最近 30 天 · 前 20 项</span>
        </div>
        <div className="breakdown-list">
          {stats.apps.length ? (
            stats.apps.map((app) => (
              <div key={app.id} className="breakdown-item">
                <div>
                  <b>{app.name}</b>
                  <small>
                    {app.active_users} 位活跃用户 · {app.logins} 次登录
                  </small>
                </div>
                <meter
                  min={0}
                  max={Math.max(1, ...stats.apps.map((a) => a.active_users))}
                  value={app.active_users}
                  aria-label={`${app.name}活跃用户`}
                />
              </div>
            ))
          ) : (
            <p className="no-results">暂无应用活跃记录</p>
          )}
        </div>
      </section>
      <section className="panel">
        <div className="panel-title">
          <h2>用户构成与安全</h2>
          <Users size={19} />
        </div>
        <div className="breakdown-list">
          {stats.roles.map((group) => (
            <div key={group.name} className="composition-row">
              <span>{roles[group.name] || group.name}</span>
              <b>{group.count} 人</b>
            </div>
          ))}
          <div className="composition-row">
            <span>已启用两步验证</span>
            <b>
              {stats.summary.tfa_users} / {stats.summary.total_users}
            </b>
          </div>
          <div className="composition-row">
            <span>停用账户</span>
            <b>{stats.summary.disabled_users} 人</b>
          </div>
        </div>
      </section>
    </div>
  );
}

function UserDetail({
  uid,
  api,
  close,
  manage,
  version,
}: {
  uid: string;
  api: API;
  close: () => void;
  manage: (user: Account) => void;
  version: number;
}) {
  const [revision, setRevision] = useState(0),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const state = useResource<Detail>(
    `/api/admin/users/${encodeURIComponent(uid)}`,
    api,
    revision + version,
  );
  const data = state.data;
  return (
    <div className="user-detail">
      <div className="detail-actions">
        <button className="button secondary" onClick={close}>
          <ArrowLeft size={15} />
          返回用户分析
        </button>
        <button
          className="button secondary"
          onClick={() => setRevision((v) => v + 1)}
        >
          <RefreshCw size={15} />
          刷新详情
        </button>
      </div>
      <ResourceStatus {...state} />
      {data && (
        <>
          <section className="panel user-summary">
            <div className="avatar">{data.user.name.slice(0, 1)}</div>
            <div className="grow">
              <h2>{data.user.name}</h2>
              <p>{data.user.email}</p>
              <span className="badge purple">{roles[data.user.role]}</span>{" "}
              <span
                className={`badge ${data.user.disabled ? "gray" : "green"}`}
              >
                {data.user.disabled
                  ? "已停用"
                  : data.analytics.summary.online_users
                    ? "在线"
                    : "离线"}
              </span>
            </div>
            <button
              className="button secondary"
              onClick={() => manage(data.user)}
            >
              管理权限
            </button>
          </section>
          <div className="stats-grid insight-stats">
            <StatCard
              label="30 天登录"
              value={data.analytics.summary.logins_30d}
              note="包含控制台与第三方应用"
              icon={LogIn}
            />
            <StatCard
              label="30 天活跃天数"
              value={data.analytics.days.filter((d) => d.active > 0).length}
              note="按 UTC 自然日去重"
              icon={Activity}
            />
            <StatCard
              label="最近访问应用"
              value={data.analytics.apps.length}
              note="最近 30 天，最多显示 20 项"
              icon={Building2}
            />
            <StatCard
              label="展示的有效会话"
              value={data.sessions.length}
              note="最多展示 50 条，在线按心跳判断"
              icon={UserRound}
            />
          </div>
          <div className="user-profile-grid">
            <section className="panel">
              <div className="panel-title">
                <h2>用户资料</h2>
                <span className="badge">用户自述</span>
              </div>
              <dl className="profile-details">
                {[
                  ["用户 ID", data.user.id],
                  ["组织 / 公司", data.profile.organization],
                  ["部门", data.profile.department],
                  ["职位", data.profile.job_title],
                  ["地区", data.profile.location],
                  ["电话（未验证）", data.profile.phone],
                  [
                    "语言 / 时区",
                    `${data.profile.locale} · ${data.profile.time_zone}`,
                  ],
                  ["注册时间", date(data.user.created_at)],
                  ["最近活跃", date(data.analytics.summary.last_seen)],
                  ["两步验证", data.user.tfa ? "已启用" : "未启用"],
                  ["个人简介", data.profile.bio],
                ].map(([key, value]) => (
                  <div key={key}>
                    <dt>{key}</dt>
                    <dd>{value || "未填写"}</dd>
                  </div>
                ))}
              </dl>
            </section>
            <section className="panel">
              <div className="panel-title">
                <h2>内部标签与备注</h2>
                <span className="badge purple">仅管理员</span>
              </div>
              <form
                className="panel-form"
                key={data.annotations.revision}
                onSubmit={async (event) => {
                  event.preventDefault();
                  if (busy) return;
                  const form = new FormData(event.currentTarget);
                  setBusy(true);
                  setError("");
                  try {
                    await api(
                      `/api/admin/users/${encodeURIComponent(uid)}/annotations`,
                      "PUT",
                      {
                        tags: String(form.get("tags") || "")
                          .split(/[,，]/)
                          .map((s) => s.trim())
                          .filter(Boolean),
                        notes: form.get("notes"),
                        expected_revision: data.annotations.revision,
                      },
                    );
                    setRevision((v) => v + 1);
                  } catch (err) {
                    setError((err as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                {error && (
                  <div className="alert error" role="alert">
                    {error}
                  </div>
                )}
                <InputField
                  label="标签"
                  name="tags"
                  value={data.annotations.tags.join("，")}
                  limit={600}
                  hint="逗号分隔，最多 12 个，每个最多 48 字节"
                />
                <label className="field">
                  <span>管理员备注</span>
                  <textarea
                    name="notes"
                    defaultValue={data.annotations.notes}
                    maxLength={2000}
                    rows={5}
                  />
                </label>
                <p className="annotation-note">
                  标签用于用户分组，不影响账户权限。请勿填写密码、密钥或不必要的敏感信息。
                </p>
                <button className="button" disabled={busy}>
                  {busy && <LoaderCircle className="spin" size={15} />}
                  保存标签与备注
                </button>
              </form>
            </section>
          </div>
          <Trend stats={data.analytics} />
          <Breakdown stats={data.analytics} />
          <section className="panel">
            <div className="panel-title">
              <h2>当前登录设备</h2>
              <span className="muted">最多 50 条</span>
            </div>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>应用</th>
                    <th>设备</th>
                    <th>IP</th>
                    <th>最后活跃</th>
                    <th>状态</th>
                  </tr>
                </thead>
                <tbody>
                  {data.sessions.map((session) => (
                    <tr key={session.id}>
                      <td>{session.app_name}</td>
                      <td className="agent" title={session.agent}>
                        {session.agent || "未知设备"}
                      </td>
                      <td>
                        <code>{session.ip}</code>
                      </td>
                      <td>{date(session.last_seen)}</td>
                      <td>
                        <span
                          className={`badge ${session.online ? "green" : "gray"}`}
                        >
                          {session.online ? "在线" : "离线"}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.sessions.length && (
                <p className="no-results">暂无有效会话</p>
              )}
            </div>
          </section>
          <section className="panel">
            <div className="panel-title">
              <h2>最近操作</h2>
              <span className="muted">最多 50 条</span>
            </div>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>事件</th>
                    <th>详情</th>
                    <th>来源 IP</th>
                    <th>时间</th>
                  </tr>
                </thead>
                <tbody>
                  {data.audits.map((audit, index) => (
                    <tr key={index}>
                      <td>
                        <span title={audit.action}>
                          {actionLabel[audit.action] || audit.action}
                        </span>
                      </td>
                      <td className="audit-detail" title={audit.detail}>
                        {audit.detail || "—"}
                      </td>
                      <td>{audit.ip}</td>
                      <td>{date(audit.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!data.audits.length && (
                <p className="no-results">暂无操作记录</p>
              )}
            </div>
          </section>
        </>
      )}
    </div>
  );
}

export function AdminInsights({
  api,
  manage,
  directoryOnly = false,
  version = 0,
}: {
  api: API;
  manage: (user: Account) => void;
  directoryOnly?: boolean;
  version?: number;
}) {
  const [revision, setRevision] = useState(0),
    [selected, setSelected] = useState<string | null>(null);
  const [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [role, setRole] = useState(""),
    [status, setStatus] = useState(""),
    [page, setPage] = useState(1);
  const detailRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setRevision((value) => value + 1);
  }, [version]);
  const stats = useResource<Stats>("/api/admin/analytics", api, revision);
  const directory = useResource<Directory>(
    `/api/admin/directory?${new URLSearchParams({ q: query, role, state: status, page: String(page) })}`,
    api,
    revision,
  );
  useEffect(() => {
    const timer = setTimeout(() => {
      setQuery(search);
      setPage(1);
    }, 250);
    return () => clearTimeout(timer);
  }, [search]);
  useEffect(() => {
    if (selected) detailRef.current?.focus();
  }, [selected]);
  if (selected)
    return (
      <div ref={detailRef} tabIndex={-1} className="detail-focus">
        <UserDetail
          key={selected}
          uid={selected}
          api={api}
          manage={manage}
          version={version}
          close={() => {
            setSelected(null);
            setRevision((v) => v + 1);
          }}
        />
      </div>
    );
  return (
    <div className="insights">
      <div className="insight-toolbar">
        <p>从整体趋势到个人画像，了解每一次身份连接。</p>
        <button
          className="button secondary"
          disabled={stats.loading || directory.loading}
          onClick={() => setRevision((v) => v + 1)}
        >
          <RefreshCw size={14} />
          刷新数据
        </button>
      </div>
      <ResourceStatus {...stats} />
      {!directoryOnly && stats.data && (
        <>
          <div className="stats-grid insight-stats">
            <StatCard
              label="全部用户"
              value={stats.data.summary.total_users}
              note={`近 30 天新增 ${stats.data.summary.new_users_30d} 人`}
              icon={Users}
            />
            <StatCard
              label="当前在线"
              value={stats.data.summary.online_users}
              note="120 秒内有有效心跳的独立用户"
              icon={Activity}
            />
            <StatCard
              label="今日活跃"
              value={stats.data.summary.active_today}
              note={`近 7 天活跃 ${stats.data.summary.active_7d} 人`}
              icon={UserRound}
            />
            <StatCard
              label="30 天活跃"
              value={stats.data.summary.active_30d}
              note={`近 30 天登录 ${stats.data.summary.logins_30d} 次`}
              icon={LogIn}
            />
          </div>
          <Trend stats={stats.data} />
          <Breakdown stats={stats.data} />
        </>
      )}
      <section className="panel">
        <div className="panel-title">
          <div>
            <h2>用户目录</h2>
            <p>查看个人资料、使用趋势、设备与内部标签</p>
          </div>
          <span className="badge">{directory.data?.total ?? "—"} 位用户</span>
        </div>
        <div className="directory-filters">
          <label className="search-input">
            <Search size={16} />
            <input
              aria-label="搜索用户资料"
              placeholder="姓名、邮箱、组织、标签"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </label>
          <select
            aria-label="筛选用户角色"
            value={role}
            onChange={(event) => {
              setRole(event.target.value);
              setPage(1);
            }}
          >
            <option value="">全部角色</option>
            {Object.entries(roles).map(([value, name]) => (
              <option key={value} value={value}>
                {name}
              </option>
            ))}
          </select>
          <select
            aria-label="筛选用户状态"
            value={status}
            onChange={(event) => {
              setStatus(event.target.value);
              setPage(1);
            }}
          >
            <option value="">全部状态</option>
            <option value="online">当前在线</option>
            <option value="enabled">正常账户</option>
            <option value="disabled">已停用</option>
          </select>
        </div>
        <ResourceStatus {...directory} />
        {directory.data && (
          <>
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>用户</th>
                    <th>组织 / 部门</th>
                    <th>角色</th>
                    <th>标签</th>
                    <th>最近活跃</th>
                    <th>状态</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {directory.data.items.map((item) => (
                    <tr key={item.user.id}>
                      <td>
                        <div className="table-app">
                          <div className="avatar">
                            {item.user.name.slice(0, 1)}
                          </div>
                          <div>
                            <b>{item.user.name}</b>
                            <small>{item.user.email}</small>
                          </div>
                        </div>
                      </td>
                      <td>
                        {item.organization || "未填写"}
                        <small>{item.department}</small>
                      </td>
                      <td>{roles[item.user.role]}</td>
                      <td>
                        <div className="scope-list">
                          {item.tags.length
                            ? item.tags.map((tag) => (
                                <span className="badge purple" key={tag}>
                                  {tag}
                                </span>
                              ))
                            : "—"}
                        </div>
                      </td>
                      <td>{date(item.last_seen)}</td>
                      <td>
                        <span
                          className={`badge ${item.online ? "green" : "gray"}`}
                        >
                          {item.user.disabled
                            ? "已停用"
                            : item.online
                              ? "在线"
                              : "离线"}
                        </span>
                      </td>
                      <td>
                        <button
                          className="text-button"
                          onClick={() => setSelected(item.user.id)}
                          aria-label={`查看${item.user.name}的用户详情`}
                        >
                          用户详情
                          <ChevronRight size={14} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!directory.data.items.length && (
                <p className="no-results">没有匹配的用户</p>
              )}
            </div>
            <div className="directory-pagination">
              <span>
                共 {directory.data.total} 人 · 第 {page} 页
              </span>
              <div>
                <button
                  className="button secondary"
                  disabled={page <= 1}
                  onClick={() => setPage((v) => v - 1)}
                >
                  <ChevronLeft size={14} />
                  上一页
                </button>
                <button
                  className="button secondary"
                  disabled={
                    page * directory.data.page_size >= directory.data.total
                  }
                  onClick={() => setPage((v) => v + 1)}
                >
                  下一页
                  <ChevronRight size={14} />
                </button>
              </div>
            </div>
          </>
        )}
      </section>
      <p className="analytics-note">
        统计更新时间：{stats.data ? date(stats.data.generated_at) : "—"}。UTC
        自然日；多设备、多应用按用户去重。心跳表示服务访问，不代表停留时长。旧数据根据保留的登录审计及会话回填，不推测缺失历史。
      </p>
    </div>
  );
}
