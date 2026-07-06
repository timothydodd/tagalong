import { useEffect, useState } from "react";
import { Link, useParams, useNavigate } from "react-router-dom";
import { api, type App, type DeployEvent, type TargetStatus } from "../api";
import { CopyField, downloadText, ErrorBox, StatusBadge, timeAgo, tagOf } from "../components";
import { useLiveEvents } from "../useEvents";

export default function AppDetail() {
  const { id } = useParams();
  const appId = Number(id);
  const nav = useNavigate();
  const [app, setApp] = useState<App | null>(null);
  const [statuses, setStatuses] = useState<TargetStatus[]>([]);
  const [initialEvents, setInitialEvents] = useState<DeployEvent[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [tags, setTags] = useState<string[] | null>(null);
  const [tag, setTag] = useState("");
  const [deploying, setDeploying] = useState(false);
  const [yamlOpen, setYamlOpen] = useState(false);
  const [yamlText, setYamlText] = useState("");
  const [yamlBusy, setYamlBusy] = useState(false);
  const [yamlMsg, setYamlMsg] = useState<string | null>(null);

  const events = useLiveEvents(initialEvents).filter((e) => e.app_id === appId);

  const loadStatus = () =>
    api.appStatus(appId).then(setStatuses).catch(() => {});

  useEffect(() => {
    api.getApp(appId).then(setApp).catch((e) => setError(String(e)));
    api.listEvents({ app_id: appId, limit: 50 }).then(setInitialEvents).catch(() => {});
    loadStatus();
    const iv = setInterval(loadStatus, 5000);
    return () => clearInterval(iv);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appId]);

  const doDeploy = async (t?: string) => {
    setDeploying(true);
    setError(null);
    try {
      await api.deploy(appId, t);
      loadStatus();
    } catch (e) {
      setError(String(e));
    } finally {
      setDeploying(false);
    }
  };

  const loadTags = async () => {
    try {
      setTags(await api.appTags(appId));
    } catch (e) {
      setError(`Load tags: ${e}`);
    }
  };

  const del = async () => {
    if (!confirm(`Delete app "${app?.name}"? History is kept.`)) return;
    await api.deleteApp(appId);
    nav("/");
  };

  const openYaml = async () => {
    setError(null);
    setYamlMsg(null);
    try {
      setYamlText(await api.exportApp(appId));
      setYamlOpen(true);
    } catch (e) {
      setError(`Load YAML: ${e}`);
    }
  };

  const applyYaml = async () => {
    setYamlBusy(true);
    setError(null);
    setYamlMsg(null);
    try {
      const updated = await api.updateAppYAML(appId, yamlText);
      setApp(updated);
      setYamlMsg("Applied.");
      loadStatus();
    } catch (e) {
      setError(`Apply YAML: ${e}`);
    } finally {
      setYamlBusy(false);
    }
  };

  if (!app) return <ErrorBox error={error} />;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>{app.name}</h1>
          <div className="sub mono">{app.image_repo}</div>
        </div>
        <div className="flex">
          <Link to={`/apps/${appId}/edit`} className="btn">
            Edit
          </Link>
          <button className="btn danger" onClick={del}>
            Delete
          </button>
        </div>
      </div>

      <ErrorBox error={error} />

      {/* Live status */}
      <div className="card">
        <div className="section-title">Live status</div>
        {statuses.length === 0 ? (
          <div className="faint">No status yet.</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Target</th>
                <th>Current image</th>
                <th>Ready</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {statuses.map((s, i) => (
                <tr key={i}>
                  <td className="mono">
                    {s.target.namespace}/{s.target.name}
                    <span className="faint"> · {s.target.container}</span>
                  </td>
                  <td>
                    <span className="tag">{tagOf(s.current_image)}</span>{" "}
                    {s.error && <span className="badge failed">error</span>}
                  </td>
                  <td className="mono">
                    {s.ready_replicas}/{s.replicas}
                  </td>
                  <td>
                    {s.error ? (
                      <span className="faint" title={s.error}>
                        {s.error.slice(0, 40)}
                      </span>
                    ) : s.available ? (
                      <span className="badge success">healthy</span>
                    ) : (
                      <span className="badge rolling pulse">updating</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Webhooks */}
      <div className="card">
        <div className="section-title">Webhooks</div>
        <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
          Point your registry at the URL for wherever it publishes. URLs use this
          portal's address — if you reach the portal on the LAN but the registry is
          external, swap the host for your public tagalong URL.
        </div>
        <div className="form-row">
          <label>Docker Hub</label>
          <CopyField value={`${window.location.origin}/hooks/dockerhub/${app.webhook_token}`} />
          <div className="hint">
            Per-app URL — the token identifies this app. Docker Hub → the repo →
            <code>Webhooks</code>.
          </div>
        </div>
        <div className="form-row" style={{ marginBottom: 0 }}>
          <label>GitHub (GHCR)</label>
          <CopyField value={`${window.location.origin}/hooks/github`} />
          <div className="hint">
            Shared URL — GitHub matches this app by <code>image_repo</code> (
            <span className="mono">{app.image_repo}</span>). Set the secret in{" "}
            <Link to="/settings">Settings</Link>, content type{" "}
            <code>application/json</code>, event <code>Packages</code>.
          </div>
        </div>
      </div>

      {/* Manual deploy */}
      <div className="card">
        <div className="section-title">Manual deploy</div>
        <div className="flex-wrap">
          <button className="btn" onClick={() => doDeploy()} disabled={deploying}>
            Rollout-restart (current image)
          </button>
          <span className="faint">or deploy a tag:</span>
          {tags === null ? (
            <button className="btn sm" onClick={loadTags}>
              Load tags…
            </button>
          ) : (
            <select value={tag} onChange={(e) => setTag(e.target.value)} style={{ width: 260 }}>
              <option value="">select a tag…</option>
              {tags.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          )}
          <button
            className="btn primary"
            disabled={!tag || deploying}
            onClick={() => doDeploy(tag)}
          >
            {deploying ? "Deploying…" : "Deploy tag"}
          </button>
        </div>
      </div>

      {/* YAML */}
      <div className="card">
        <div className="flex" style={{ marginBottom: yamlOpen ? 12 : 0 }}>
          <div className="section-title" style={{ margin: 0 }}>
            Configuration (YAML)
          </div>
          <div className="spacer" />
          {!yamlOpen ? (
            <button className="btn sm" onClick={openYaml}>
              Edit as YAML
            </button>
          ) : (
            <button
              className="btn sm"
              onClick={() => downloadText(`tagalong-${app.name}.yaml`, yamlText)}
            >
              Download
            </button>
          )}
        </div>
        {yamlOpen && (
          <>
            <div className="hint" style={{ marginBottom: 8 }}>
              Edits replace this app's config. <code>image_repo</code> is normalized and{" "}
              <code>webhook_token</code> is preserved if you leave it blank.
            </div>
            <textarea
              className="mono"
              style={{ minHeight: 300 }}
              value={yamlText}
              onChange={(e) => setYamlText(e.target.value)}
            />
            {yamlMsg && <div className="ok-box" style={{ marginTop: 10 }}>{yamlMsg}</div>}
            <div className="flex" style={{ marginTop: 10 }}>
              <button className="btn primary" onClick={applyYaml} disabled={yamlBusy}>
                {yamlBusy ? "Applying…" : "Apply YAML"}
              </button>
              <button
                className="btn"
                onClick={() => {
                  setYamlOpen(false);
                  setYamlMsg(null);
                }}
                disabled={yamlBusy}
              >
                Close
              </button>
            </div>
          </>
        )}
      </div>

      {/* History */}
      <div className="card" style={{ padding: 0 }}>
        <div className="section-title" style={{ padding: "16px 20px 0" }}>
          History
        </div>
        {events.length === 0 ? (
          <div className="empty">No deploys yet.</div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Trigger</th>
                <th>Change</th>
                <th>Status</th>
                <th className="right"></th>
              </tr>
            </thead>
            <tbody>
              {events.map((e) => (
                <tr key={e.id}>
                  <td title={e.started_at}>{timeAgo(e.started_at)}</td>
                  <td>
                    <span className="tag">{e.trigger}</span>
                  </td>
                  <td>
                    {e.action === "restart" ? (
                      <span className="muted">restart</span>
                    ) : (
                      <span className="mono" style={{ fontSize: 12 }}>
                        {e.old_image ? tagOf(e.old_image) : "—"}{" "}
                        <span className="faint">→</span> {tagOf(e.new_image)}
                      </span>
                    )}
                    {e.cf_purged && <span className="tag" style={{ marginLeft: 6 }}>cf purged</span>}
                    {e.detail && (
                      <div className="faint" style={{ fontSize: 11.5, marginTop: 3 }}>
                        {e.detail}
                      </div>
                    )}
                  </td>
                  <td>
                    <StatusBadge status={e.status} />
                  </td>
                  <td className="right">
                    {e.action === "patch" && e.old_image && e.status === "success" && (
                      <button
                        className="btn sm"
                        title={`Roll back to ${tagOf(e.old_image)}`}
                        onClick={() => doDeploy(tagOf(e.old_image))}
                      >
                        Rollback
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
