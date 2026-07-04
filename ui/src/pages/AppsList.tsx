import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, type App } from "../api";
import { downloadText, ErrorBox, StatusBadge, tagOf } from "../components";
import type { DeployEvent } from "../api";
import { useEventStream } from "../useEvents";

export default function AppsList() {
  const [apps, setApps] = useState<App[]>([]);
  const [lastEvent, setLastEvent] = useState<Record<number, DeployEvent>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<number | null>(null);
  const [importing, setImporting] = useState(false);
  const [importText, setImportText] = useState("");
  const [importBusy, setImportBusy] = useState(false);
  const [importMsg, setImportMsg] = useState<string | null>(null);
  const nav = useNavigate();

  const load = async () => {
    try {
      const list = await api.listApps();
      setApps(list);
      const events = await api.listEvents({ limit: 100 });
      const byApp: Record<number, DeployEvent> = {};
      for (const e of events) {
        if (e.app_id && !byApp[e.app_id]) byApp[e.app_id] = e;
      }
      setLastEvent(byApp);
    } catch (e) {
      setError(String(e));
    }
  };

  useEffect(() => {
    load();
  }, []);

  // Live-update the last-event column as deploys happen.
  useEventStream((e) => {
    if (e.app_id) setLastEvent((prev) => ({ ...prev, [e.app_id!]: e }));
  });

  const deploy = async (app: App) => {
    setBusy(app.id);
    setError(null);
    try {
      await api.deploy(app.id); // no tag = rollout-restart
    } catch (e) {
      setError(`Deploy ${app.name}: ${e}`);
    } finally {
      setBusy(null);
    }
  };

  const toggleEnabled = async (app: App) => {
    try {
      await api.updateApp(app.id, { ...app, enabled: !app.enabled });
      load();
    } catch (e) {
      setError(String(e));
    }
  };

  const exportYAML = async () => {
    setError(null);
    try {
      downloadText("tagalong-apps.yaml", await api.exportApps());
    } catch (e) {
      setError(`Export: ${e}`);
    }
  };

  const runImport = async () => {
    setImportBusy(true);
    setImportMsg(null);
    setError(null);
    try {
      const res = await api.importApps(importText);
      setImportMsg(`Imported: ${res.created} created, ${res.updated} updated.`);
      setImportText("");
      setImporting(false);
      load();
    } catch (e) {
      setError(`Import: ${e}`);
    } finally {
      setImportBusy(false);
    }
  };

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Apps</h1>
          <div className="sub">Configured deployments tagalong watches and updates.</div>
        </div>
        <div className="flex">
          <button className="btn" onClick={() => setImporting((v) => !v)}>
            Import YAML
          </button>
          <button className="btn" onClick={exportYAML} disabled={apps.length === 0}>
            Export YAML
          </button>
          <Link to="/apps/new" className="btn primary">
            + New app
          </Link>
        </div>
      </div>

      <ErrorBox error={error} />
      {importMsg && <div className="ok-box">{importMsg}</div>}

      {importing && (
        <div className="card">
          <div className="section-title">Import apps from YAML</div>
          <div className="hint" style={{ marginBottom: 8 }}>
            Paste a <code>tagalong-apps.yaml</code> file (top-level <code>apps:</code> list).
            Apps are matched by <b>name</b>: existing are updated, new are created — nothing
            is deleted.
          </div>
          <textarea
            className="mono"
            style={{ minHeight: 220 }}
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
            placeholder={"apps:\n  - name: robo-dash\n    image_repo: timdoddcool/robo-dash\n    tag_strategy: latest\n    enabled: true\n    targets:\n      - namespace: default\n        name: homedash\n        container: robo-dash"}
          />
          <div className="flex" style={{ marginTop: 10 }}>
            <button
              className="btn primary"
              onClick={runImport}
              disabled={importBusy || !importText.trim()}
            >
              {importBusy ? "Applying…" : "Apply"}
            </button>
            <button className="btn" onClick={() => setImporting(false)} disabled={importBusy}>
              Cancel
            </button>
          </div>
        </div>
      )}

      <div className="card" style={{ padding: 0 }}>
        {apps.length === 0 ? (
          <div className="empty">
            No apps yet. <Link to="/apps/new">Create your first app</Link> to start
            auto-deploying.
          </div>
        ) : (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Image</th>
                <th>Strategy</th>
                <th>Last deploy</th>
                <th style={{ width: 90 }}>Enabled</th>
                <th className="right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {apps.map((app) => {
                const ev = lastEvent[app.id];
                return (
                  <tr key={app.id}>
                    <td>
                      <Link to={`/apps/${app.id}`} style={{ fontWeight: 600 }}>
                        {app.name}
                      </Link>
                      <div className="faint mono" style={{ fontSize: 11 }}>
                        {app.targets?.length ?? 0} target{(app.targets?.length ?? 0) !== 1 ? "s" : ""}
                      </div>
                    </td>
                    <td>
                      <span className="mono muted">{app.image_repo}</span>
                      {app.last_seen_tag && (
                        <>
                          {" "}
                          <span className="tag">{app.last_seen_tag}</span>
                        </>
                      )}
                    </td>
                    <td>
                      <span className="tag strat-badge">{app.tag_strategy}</span>
                    </td>
                    <td>
                      {ev ? (
                        <div className="flex">
                          <StatusBadge status={ev.status} />
                          {ev.new_image && (
                            <span className="tag">{tagOf(ev.new_image)}</span>
                          )}
                        </div>
                      ) : (
                        <span className="faint">never</span>
                      )}
                    </td>
                    <td>
                      <label className="switch">
                        <input
                          type="checkbox"
                          checked={app.enabled}
                          onChange={() => toggleEnabled(app)}
                        />
                        <span className="slider" />
                      </label>
                    </td>
                    <td className="right">
                      <div className="flex" style={{ justifyContent: "flex-end" }}>
                        <button
                          className="btn sm"
                          disabled={busy === app.id}
                          onClick={() => deploy(app)}
                        >
                          {busy === app.id ? "…" : "Redeploy"}
                        </button>
                        <button
                          className="btn sm"
                          onClick={() => nav(`/apps/${app.id}/edit`)}
                        >
                          Edit
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
