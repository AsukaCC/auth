import { useCallback, useEffect, useRef, useState } from "react";
import { Check, DownloadCloud, LoaderCircle, RefreshCw, RotateCcw, ShieldCheck, ExternalLink } from "lucide-react";

type API = (path: string, method?: string, data?: unknown) => Promise<any>;
type Image = { version: string; id: string; schema: number; health: string };
type Release = { version: string; schema: number; sha256: string; size: number };
type Job = { id: string; status: string; stage: string; message: string; updated_at: number; request: { action: string; version: string } };
type Status = { enabled: boolean; connected: boolean; repository: string; platform: string; current: Image | null; previous: Image | null; managed: boolean; pending: boolean; locked: boolean; rollback_allowed: boolean; update_available: boolean; release: Release | null; job: Job | null; message: string };
type Action = "update" | "rollback" | "recover";
const names: Record<Action, string> = { update: "更新版本", rollback: "回退版本", recover: "恢复部署" };
const stages = [{ key: "queued", name: "接收任务" }, { key: "download", name: "下载校验" }, { key: "deploy", name: "备份与切换" }, { key: "health", name: "健康检查" }, { key: "done", name: "完成" }];
const ongoing = (job?: Job | null) => !!job && ["queued", "running"].includes(job.status);

export function Deployment({ api, tfa }: { api: API; tfa: boolean }) {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState("");
  const [disconnected, setDisconnected] = useState(false);
  const [checking, setChecking] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [action, setAction] = useState<Action | null>(null);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [allowMigration, setAllowMigration] = useState(false);
  const [accepted, setAccepted] = useState<Job | null>(null);
  const intent = useRef<{ key: string; current: string; previous: string; release: Release | null } | null>(null);
  const confirmRef = useRef<HTMLHeadingElement>(null);
  const mounted = useRef(true);
  const polling = useRef(false);
  const refresh = useCallback(async () => {
    if (polling.current) return;
    polling.current = true;
    try {
      const next: Status = await api("/api/admin/deployment");
      if (mounted.current) { setStatus(next); setDisconnected(next.enabled && !next.connected); }
    } catch (e) {
      if (mounted.current) { setDisconnected(true); if ((e as { status?: number }).status === 401) setError("登录已失效，请刷新页面后重新登录。"); }
    } finally { polling.current = false; }
  }, [api]);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    const timer = window.setInterval(() => void refresh(), 4000);
    return () => { mounted.current = false; window.clearInterval(timer); };
  }, [refresh]);
  useEffect(() => { if (action) confirmRef.current?.focus(); }, [action]);
  const job = status?.job || accepted;
  const busy = submitting || ongoing(job) || !!status?.locked;
  const migration = !!action && action === "update" && !!intent.current?.release && !!status?.current && intent.current.release.schema > status.current.schema;
  const open = (next: Action) => {
    if (!status?.current) return;
    intent.current = { key: crypto.randomUUID(), current: status.current.id, previous: status.previous?.id || "", release: status.release };
    setPassword(""); setCode(""); setAllowMigration(false); setError(""); setAction(next);
  };
  const check = async () => {
    setChecking(true); setError("");
    try { await api("/api/admin/deployment", "POST", { action: "check" }); await refresh(); }
    catch (e) { if (mounted.current) setError((e as Error).message); }
    finally { if (mounted.current) setChecking(false); }
  };
  return <div className="deployment-page">
    {error && <div className="alert error" role="alert">{error}</div>}
    {disconnected && <div className="alert warning" role="status">暂时无法连接服务或部署代理，正在自动重连。更新期间网页可能短暂中断；请先确认任务状态，再决定是否重试。</div>}
    {!status && !disconnected && <div className="insight-loading" role="status"><LoaderCircle className="spin" size={18} />正在读取部署状态…</div>}
    {status && <>
      <section className="panel deployment-overview">
        <div className="deployment-symbol"><DownloadCloud size={28} /></div>
        <div className="grow"><div className="eyebrow">SYSTEM UPDATES</div><h2>让身份服务保持最新</h2><p>检查正式发布版本，在这里完成更新与兼容回退。</p></div>
        <button className="button" disabled={!status.connected || checking || busy} onClick={() => void check()}><RefreshCw size={16} className={checking ? "spin" : ""} />{checking ? "正在检查" : "检查新版本"}</button>
      </section>
      {!status.enabled ? <section className="panel deployment-help"><h2>先启用部署代理</h2><p>服务器完成一次配置后，管理员即可在此执行后续更新。代理与身份服务独立运行，负责备份、切换版本和检查健康。</p><p>配置步骤见安装包中的 <code>docs/web-updates.md</code>。仅推送源码不会生成可更新版本；发布时需递增 VERSION，等待 GitHub Release 构建完成。</p></section> : <>
        {status.message && <div className="alert info" role="status">{status.message}</div>}
        <div className="deployment-versions">
          <section className="panel"><span className="muted">当前运行版本</span><h2>{status.current?.version || "无法读取"}</h2><p><span className={`badge ${status.current?.health === "healthy" ? "green" : "amber"}`}>{status.current?.health === "healthy" ? "服务健康" : status.current?.health === "stopped" ? "已停止" : "等待健康确认"}</span></p><small>数据库结构 {status.current?.schema ?? "—"} · {status.platform || "—"}</small></section>
          <section className="panel"><span className="muted">最新正式发布</span><h2>{status.release?.version || "尚未检查"}</h2><p>{status.release ? status.release.version === status.current?.version ? "已在使用当前发布版本" : status.update_available ? "有新版本可用" : "当前版本、结构或运行状态不满足更新条件" : "点击检查，获取适用于此服务器的版本"}</p>{status.release && <small>{(status.release.size / 1024 / 1024).toFixed(1)} MB · 数据库结构 {status.release.schema}</small>}<button className="button" disabled={!status.update_available || busy || disconnected} onClick={() => open("update")}><DownloadCloud size={15} />准备更新</button></section>
        </div>
        {job && <section className="panel deployment-job" aria-live="polite">
          <div className="panel-title"><div><h2>最近部署任务</h2><p>{names[job.request.action as Action] || job.request.action}{job.request.version ? ` · ${job.request.version}` : ""}</p></div><span className={`badge ${job.status === "succeeded" ? "green" : ["failed", "interrupted"].includes(job.status) ? "amber" : "blue"}`}>{({queued:"等待执行",running:"进行中",succeeded:"已完成",failed:"未完成",interrupted:"已中断"} as Record<string,string>)[job.status] || job.status}</span></div>
          <ol className="deployment-steps">{stages.map((step, index) => {
            const current = job.stage === "preflight" ? 0 : stages.findIndex(s => s.key === job.stage);
            const done = job.status === "succeeded" || current > index;
            return <li key={step.key} className={done ? "done" : current === index && ongoing(job) ? "current" : ""}><span>{done ? <Check size={14} /> : index + 1}</span>{step.name}</li>;
          })}</ol><p className="deployment-job-message">{job.message}</p><small className="deployment-job-id">任务 {job.id} · {new Date(job.updated_at * 1000).toLocaleString("zh-CN")}</small>
        </section>}
        <section className="panel deployment-recovery"><div className="grow"><h2>兼容回退与恢复</h2><p>{status.previous ? `上一版本：${status.previous.version}，数据库结构 ${status.previous.schema}。` : "还没有上一版本记录。"}回退保留当前数据库，仅切换兼容镜像。跨结构升级后无法回退旧结构。</p>{!status.managed && <p>首次网页更新会接管当前单实例部署，并保留原镜像用于兼容回退。</p>}{status.pending && <p>存在中断记录。恢复将按结构保护规则选择镜像；若发布锁仍存在，请先由服务器管理员核实运行进程。</p>}</div><div className="deployment-recovery-actions"><button className="button secondary" disabled={!status.rollback_allowed || busy || disconnected} onClick={() => open("rollback")}><RotateCcw size={15} />回退上一版本</button><button className="button secondary" disabled={!status.pending || busy || disconnected} onClick={() => open("recover")}><ShieldCheck size={15} />恢复中断部署</button></div></section>
        {action && <section className="panel deployment-confirm"><h2 ref={confirmRef} tabIndex={-1}>确认{names[action]}</h2><p>{action === "update" ? `将更新到 ${intent.current?.release?.version}，执行前会停服备份。` : action === "rollback" ? `将切换到 ${status.previous?.version}，保留当前数据。` : "按已有恢复记录重新启动兼容镜像，不覆盖数据库。"}请再次验证管理员身份。</p><form onSubmit={async event => {
          event.preventDefault(); const current = intent.current; if (!current || submitting) return;
          setSubmitting(true); setError("");
          try {
            const accepted: Job = await api("/api/admin/deployment", "POST", { action, version: action === "update" ? current.release?.version : "", sha256: action === "update" ? current.release?.sha256 : "", expected_image: current.current, target_image: action === "rollback" ? current.previous : "", idempotency_key: current.key, allow_schema_upgrade: allowMigration, password, code });
            if (mounted.current) { setAccepted(accepted); setAction(null); await refresh(); }
          } catch (e) { if (mounted.current) setError((e as Error).message); }
          finally { if (mounted.current) { setPassword(""); setCode(""); setSubmitting(false); } }
        }}>
          <label className="field"><span>管理员密码</span><input type="password" autoComplete="current-password" required value={password} onChange={e => setPassword(e.target.value)} maxLength={72} /></label>
          {tfa && <label className="field"><span>两步验证码或备用码</span><input autoComplete="one-time-code" required maxLength={20} value={code} onChange={e => setCode(e.target.value)} /></label>}
          {migration && <label className="scope-option"><input type="checkbox" required checked={allowMigration} onChange={e => setAllowMigration(e.target.checked)} /><span>我确认此次升级会迁移数据库，升级后不能回退旧结构镜像。</span></label>}
          <div className="form-actions"><button className="button" disabled={submitting || busy || disconnected || migration && !allowMigration}>{submitting && <LoaderCircle className="spin" size={15} />}确认{names[action]}</button><button type="button" className="button secondary" disabled={submitting} onClick={() => {setAction(null);setPassword("");setCode("");}}>取消</button></div>
        </form></section>}
        <div className="deployment-source"><span>更新源：{status.repository}</span>{/^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/.test(status.repository) && <a className="text-button" href={`https://github.com/${status.repository}/releases`} target="_blank" rel="noreferrer">查看发布记录<ExternalLink size={13} /></a>}<button className="text-button" disabled={submitting} onClick={() => void refresh()}>刷新部署状态</button></div>
      </>}
    </>}
  </div>;
}
