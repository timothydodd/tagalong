import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, type App, type Target, type TagStrategy } from "../api";
import { CopyField, ErrorBox } from "../components";

const PATTERN_PRESETS: { label: string; value: string }[] = [
  { label: "Full git SHA (40 hex)", value: "^[0-9a-f]{40}$" },
  { label: "Short SHA (7–12 hex)", value: "^[0-9a-f]{7,12}$" },
  { label: "metadata-action (sha-…)", value: "^sha-[0-9a-f]+$" },
  { label: "Custom…", value: "" },
];

const emptyApp = (): Partial<App> => ({
  name: "",
  image_repo: "",
  tag_strategy: "exact",
  strategy_conf: { pattern: "^[0-9a-f]{40}$" },
  targets: [{ namespace: "default", kind: "Deployment", name: "", container: "" }],
  poll_enabled: false,
  poll_interval_sec: 300,
  cf_purge: { enabled: false, mode: "everything", urls: [] },
  enabled: true,
});

export default function AppForm() {
  const { id } = useParams();
  const editing = Boolean(id);
  const nav = useNavigate();
  const [app, setApp] = useState<Partial<App>>(emptyApp());
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [presetIdx, setPresetIdx] = useState(0);

  useEffect(() => {
    if (!id) return;
    api
      .getApp(Number(id))
      .then((a) => {
        setApp(a);
        const idx = PATTERN_PRESETS.findIndex((p) => p.value === a.strategy_conf?.pattern);
        setPresetIdx(idx >= 0 ? idx : PATTERN_PRESETS.length - 1);
      })
      .catch((e) => setError(String(e)));
  }, [id]);

  const set = (patch: Partial<App>) => setApp((a) => ({ ...a, ...patch }));
  const setConf = (patch: Partial<App["strategy_conf"]>) =>
    setApp((a) => ({ ...a, strategy_conf: { ...a.strategy_conf, ...patch } }));
  const setCF = (patch: Partial<App["cf_purge"]>) =>
    setApp((a) => ({ ...a, cf_purge: { ...a.cf_purge!, ...patch } }));

  const setTarget = (i: number, patch: Partial<Target>) =>
    setApp((a) => {
      const targets = (a.targets ?? []).slice();
      targets[i] = { ...targets[i], ...patch };
      return { ...a, targets };
    });
  const addTarget = () =>
    setApp((a) => ({
      ...a,
      targets: [
        ...(a.targets ?? []),
        { namespace: "default", kind: "Deployment", name: "", container: "" },
      ],
    }));
  const removeTarget = (i: number) =>
    setApp((a) => ({ ...a, targets: (a.targets ?? []).filter((_, j) => j !== i) }));

  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const saved = editing
        ? await api.updateApp(Number(id), app)
        : await api.createApp(app);
      nav(`/apps/${saved.id}`);
    } catch (e) {
      setError(String(e));
      setSaving(false);
    }
  };

  const strat = app.tag_strategy as TagStrategy;
  const origin = window.location.origin;

  return (
    <>
      <div className="page-head">
        <h1>{editing ? `Edit ${app.name}` : "New app"}</h1>
      </div>
      <ErrorBox error={error} />

      <div className="card">
        <div className="row-2">
          <div className="form-row">
            <label>Name</label>
            <input
              type="text"
              value={app.name ?? ""}
              onChange={(e) => set({ name: e.target.value })}
              placeholder="robo-dash"
            />
          </div>
          <div className="form-row">
            <label>Image repo</label>
            <input
              type="text"
              value={app.image_repo ?? ""}
              onChange={(e) => set({ image_repo: e.target.value })}
              placeholder="timdoddcool/robo-dash"
            />
            <div className="hint">
              Registry path without a tag. Normalized on save (e.g.{" "}
              <code>docker.io/timdoddcool/robo-dash</code>).
            </div>
          </div>
        </div>

        <div className="form-row">
          <label>Tag strategy</label>
          <div className="row-2">
            <select
              value={strat}
              onChange={(e) => set({ tag_strategy: e.target.value as TagStrategy })}
            >
              <option value="exact">exact — deploy tags matching a pattern</option>
              <option value="semver">semver — deploy newer versions</option>
              <option value="latest">latest — restart on rolling-tag change</option>
            </select>
          </div>
        </div>

        {(strat === "exact" || strat === "regex") && (
          <div className="row-2">
            <div className="form-row">
              <label>Pattern preset</label>
              <select
                value={presetIdx}
                onChange={(e) => {
                  const i = Number(e.target.value);
                  setPresetIdx(i);
                  if (PATTERN_PRESETS[i].value) setConf({ pattern: PATTERN_PRESETS[i].value });
                }}
              >
                {PATTERN_PRESETS.map((p, i) => (
                  <option key={i} value={i}>
                    {p.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-row">
              <label>Regex</label>
              <input
                type="text"
                className="mono"
                value={app.strategy_conf?.pattern ?? ""}
                onChange={(e) => setConf({ pattern: e.target.value })}
              />
            </div>
          </div>
        )}

        {strat === "latest" && (
          <div className="form-row" style={{ maxWidth: 300 }}>
            <label>Tracked tag</label>
            <input
              type="text"
              value={app.strategy_conf?.track_tag ?? ""}
              onChange={(e) => setConf({ track_tag: e.target.value })}
              placeholder="latest"
            />
            <div className="hint">
              Restarts the workload when this tag's digest changes. Requires{" "}
              <code>imagePullPolicy: Always</code>.
            </div>
          </div>
        )}

        {strat === "semver" && (
          <div className="form-row" style={{ maxWidth: 300 }}>
            <label>Constraint (optional)</label>
            <input
              type="text"
              value={app.strategy_conf?.constraint ?? ""}
              onChange={(e) => setConf({ constraint: e.target.value })}
              placeholder=">=0.6"
            />
          </div>
        )}
      </div>

      {/* Targets */}
      <div className="card">
        <div className="flex" style={{ marginBottom: 12 }}>
          <div className="section-title" style={{ margin: 0 }}>
            Targets
          </div>
          <div className="spacer" />
          <button className="btn sm" onClick={addTarget}>
            + Add target
          </button>
        </div>
        {(app.targets ?? []).map((t, i) => (
          <div className="row-4" key={i} style={{ marginBottom: 10 }}>
            <div>
              {i === 0 && <label className="faint" style={{ fontSize: 11 }}>Namespace</label>}
              <input
                type="text"
                value={t.namespace}
                onChange={(e) => setTarget(i, { namespace: e.target.value })}
              />
            </div>
            <div>
              {i === 0 && <label className="faint" style={{ fontSize: 11 }}>Kind</label>}
              <select
                value={t.kind}
                onChange={(e) => setTarget(i, { kind: e.target.value as Target["kind"] })}
              >
                <option>Deployment</option>
                <option>StatefulSet</option>
              </select>
            </div>
            <div>
              {i === 0 && <label className="faint" style={{ fontSize: 11 }}>Name</label>}
              <input
                type="text"
                value={t.name}
                placeholder="homedash"
                onChange={(e) => setTarget(i, { name: e.target.value })}
              />
            </div>
            <div>
              {i === 0 && <label className="faint" style={{ fontSize: 11 }}>Container</label>}
              <input
                type="text"
                value={t.container}
                placeholder="robo-dash"
                onChange={(e) => setTarget(i, { container: e.target.value })}
              />
            </div>
            <button
              className="btn sm danger"
              onClick={() => removeTarget(i)}
              disabled={(app.targets?.length ?? 0) <= 1}
            >
              ✕
            </button>
          </div>
        ))}
      </div>

      {/* Polling */}
      <div className="card">
        <div className="section-title">Polling (fallback)</div>
        <div className="flex-wrap">
          <label className="check">
            <input
              type="checkbox"
              checked={app.poll_enabled ?? false}
              onChange={(e) => set({ poll_enabled: e.target.checked })}
            />
            Poll the registry for updates
          </label>
          {app.poll_enabled && (
            <div className="flex">
              <span className="faint">every</span>
              <input
                type="number"
                style={{ width: 90 }}
                min={60}
                value={app.poll_interval_sec ?? 300}
                onChange={(e) => set({ poll_interval_sec: Number(e.target.value) })}
              />
              <span className="faint">sec (min 60)</span>
            </div>
          )}
        </div>
      </div>

      {/* Cloudflare purge */}
      <div className="card">
        <div className="section-title">Cloudflare cache purge (after deploy)</div>
        <label className="check" style={{ marginBottom: 12 }}>
          <input
            type="checkbox"
            checked={app.cf_purge?.enabled ?? false}
            onChange={(e) => setCF({ enabled: e.target.checked })}
          />
          Purge Cloudflare cache after a successful rollout
        </label>
        {app.cf_purge?.enabled && (
          <>
            <div className="row-2">
              <div className="form-row">
                <label>Zone ID</label>
                <input
                  type="text"
                  className="mono"
                  value={app.cf_purge?.zone_id ?? ""}
                  onChange={(e) => setCF({ zone_id: e.target.value })}
                />
              </div>
              <div className="form-row">
                <label>Mode</label>
                <select
                  value={app.cf_purge?.mode ?? "everything"}
                  onChange={(e) => setCF({ mode: e.target.value as "everything" | "urls" })}
                >
                  <option value="everything">Purge everything</option>
                  <option value="urls">Purge specific URLs</option>
                </select>
              </div>
            </div>
            {app.cf_purge?.mode === "urls" && (
              <div className="form-row">
                <label>URLs (one per line)</label>
                <textarea
                  value={(app.cf_purge?.urls ?? []).join("\n")}
                  onChange={(e) =>
                    setCF({
                      urls: e.target.value
                        .split("\n")
                        .map((u) => u.trim())
                        .filter(Boolean),
                    })
                  }
                  placeholder="https://robo.dodd.rocks/"
                />
              </div>
            )}
            <div className="hint">
              Requires a Cloudflare API token in <b>Settings</b>. Free plan supports
              purge-everything and URL lists (chunked at 30).
            </div>
          </>
        )}
      </div>

      {/* Webhook URLs (edit mode only) */}
      {editing && app.webhook_token && (
        <div className="card">
          <div className="section-title">Webhook URLs</div>
          <div className="form-row">
            <label>Docker Hub</label>
            <CopyField value={`${origin}/hooks/dockerhub/${app.webhook_token}`} />
          </div>
          <div className="form-row" style={{ marginBottom: 0 }}>
            <label>GitHub (shared endpoint)</label>
            <CopyField value={`${origin}/hooks/github`} />
            <div className="hint">
              Set the GitHub webhook secret in Settings; select the <b>Packages</b> event.
            </div>
          </div>
        </div>
      )}

      <div className="flex">
        <button className="btn primary" onClick={save} disabled={saving}>
          {saving ? "Saving…" : editing ? "Save changes" : "Create app"}
        </button>
        <button className="btn" onClick={() => nav(-1)}>
          Cancel
        </button>
      </div>
    </>
  );
}
