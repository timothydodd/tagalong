import { useEffect, useState } from "react";
import { api, webhookBase, type Agent, type HubStatus } from "../api";
import { CopyField, errMsg, ErrorBox, timeAgo } from "../components";

// How often the cards refresh live connection state.
const REFRESH_MS = 10_000;

function ConnBadge({ connected }: { connected: boolean }) {
  return (
    <span className={`badge ${connected ? "success" : "skipped"}`}>
      {connected ? "connected" : "offline"}
    </span>
  );
}

// HubCard configures this instance as an agent of a hub, and shows the live
// connection. Read-only when the env vars manage it.
export function HubCard() {
  const [status, setStatus] = useState<HubStatus | null>(null);
  // Form fields load once, so the periodic status refresh never clobbers edits.
  const [url, setUrl] = useState("");
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    api
      .hubStatus()
      .then((s) => {
        setStatus(s);
        setUrl(s.url ?? "");
        setToken(s.token ?? "");
      })
      .catch((e) => setError(errMsg(e)));
    const t = setInterval(() => api.hubStatus().then(setStatus).catch(() => {}), REFRESH_MS);
    return () => clearInterval(t);
  }, []);

  const apply = async (nextUrl: string, nextToken: string) => {
    setError(null);
    try {
      const s = await api.putHub(nextUrl, nextToken);
      setStatus(s);
      setUrl(s.url ?? "");
      setToken(s.token ?? "");
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      // The first poll lands within a moment; refresh so the badge catches up.
      setTimeout(() => api.hubStatus().then(setStatus).catch(() => {}), 1500);
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const disconnect = () => {
    if (!confirm("Disconnect from the hub? Webhooks it receives will stop reaching this instance.")) return;
    apply("", "");
  };

  const locked = status?.locked ?? false;
  return (
    <div className="card">
      <div className="section-title">Hub connection</div>
      <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
        Run this instance as an <b>agent</b> of a public tagalong (the hub): it connects out to
        the hub and handles every webhook the hub receives, using this instance&rsquo;s own apps.
        Nothing here needs to be exposed. Create the agent and its token in the hub&rsquo;s{" "}
        <b>Settings &rarr; Agents</b>.
      </div>
      <ErrorBox error={error} />
      {locked && (
        <div className="warn-box">
          Set by <code>TAGALONG_HUB_URL</code> / <code>TAGALONG_AGENT_TOKEN</code> on this
          instance &mdash; change it there (and restart) instead.
        </div>
      )}
      <div className="row-2">
        <div className="form-row">
          <label>Hub URL</label>
          <input
            type="text"
            value={url}
            disabled={locked}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://tagalong.example.com"
          />
        </div>
        <div className="form-row">
          <label>Agent token</label>
          <input
            type="password"
            value={token}
            disabled={locked}
            onChange={(e) => setToken(e.target.value)}
            placeholder="(from the hub)"
            autoComplete="off"
          />
        </div>
      </div>
      {!locked && (
        <div style={{ display: "flex", gap: 8, marginBottom: status?.enabled ? 14 : 0 }}>
          <button className="btn primary" onClick={() => apply(url, token)} disabled={!url.trim()}>
            {saved ? "Saved ✓" : status?.enabled ? "Save & reconnect" : "Connect"}
          </button>
          {status?.enabled && (
            <button className="btn danger" onClick={disconnect}>
              Disconnect
            </button>
          )}
        </div>
      )}

      {status?.enabled && (
        <div className="table-wrap">
          <table>
            <tbody>
              <tr>
                <td>Status</td>
                <td>
                  <ConnBadge connected={status.connected} />
                </td>
              </tr>
              <tr>
                <td>Last contact</td>
                <td>{timeAgo(status.last_contact)}</td>
              </tr>
              <tr>
                <td>Webhooks relayed</td>
                <td>{status.relayed} since connecting</td>
              </tr>
              {status.last_error && (
                <tr>
                  <td>Last error</td>
                  <td className="faint">{status.last_error}</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
      {!locked && (
        <div className="hint" style={{ marginTop: 8 }}>
          The token is masked as <code>********</code> &mdash; leave it masked to keep it unchanged.
        </div>
      )}
    </div>
  );
}

// AgentsCard manages the agents registered with this instance (the hub).
export function AgentsCard({ publicBaseURL }: { publicBaseURL: string }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [name, setName] = useState("");
  const [created, setCreated] = useState<{ name: string; token: string } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = () => api.listAgents().then(setAgents).catch((e) => setError(errMsg(e)));
  useEffect(() => {
    load();
    const t = setInterval(load, REFRESH_MS);
    return () => clearInterval(t);
  }, []);

  const add = async () => {
    setError(null);
    try {
      const a = await api.createAgent(name.trim());
      setCreated({ name: a.name, token: a.token });
      setName("");
      load();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const remove = async (a: Agent) => {
    if (!confirm(`Remove agent "${a.name}"? Its token stops working immediately.`)) return;
    try {
      await api.deleteAgent(a.id);
      if (created?.name === a.name) setCreated(null);
      load();
    } catch (e) {
      setError(`Remove agent: ${errMsg(e)}`);
    }
  };

  return (
    <div className="card">
      <div className="section-title">Agents</div>
      <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
        Other tagalong instances (e.g. on an internal network) that connect out to this one.
        <b>Every</b> webhook this instance receives is also passed to every agent, which
        deploys it with its own apps to its own cluster. Agents never need to be exposed.
      </div>
      <ErrorBox error={error} />

      {created && (
        <div className="warn-box">
          <div style={{ marginBottom: 8 }}>
            Agent <b>{created.name}</b> created. Paste these into the agent&rsquo;s{" "}
            <b>Settings &rarr; Hub connection</b> (or set them as env vars) &mdash; the token is{" "}
            <b>shown only once</b>.
          </div>
          <div className="form-row">
            <label>Hub URL <span className="faint">(TAGALONG_HUB_URL)</span></label>
            <CopyField value={webhookBase(publicBaseURL)} />
          </div>
          <div className="form-row">
            <label>Agent token <span className="faint">(TAGALONG_AGENT_TOKEN)</span></label>
            <CopyField value={created.token} />
          </div>
          <button className="btn sm" onClick={() => setCreated(null)}>
            Done
          </button>
        </div>
      )}

      {agents.length > 0 && (
        <div className="table-wrap" style={{ marginBottom: 14 }}>
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Status</th>
                <th>Last seen</th>
                <th>Queued</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {agents.map((a) => (
                <tr key={a.id}>
                  <td className="mono">{a.name}</td>
                  <td>
                    <ConnBadge connected={a.connected} />
                  </td>
                  <td>{timeAgo(a.last_seen)}</td>
                  <td className={a.queued ? "" : "faint"}>{a.queued}</td>
                  <td className="right">
                    <button className="btn sm danger" onClick={() => remove(a)}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="row-4" style={{ gridTemplateColumns: "1fr auto" }}>
        <input
          type="text"
          placeholder="agent name, e.g. office"
          value={name}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && name.trim() && add()}
        />
        <button className="btn" onClick={add} disabled={!name.trim()}>
          Add agent
        </button>
      </div>
      <div className="hint" style={{ marginTop: 8 }}>
        &ldquo;Last seen&rdquo; and &ldquo;Queued&rdquo; reset when this instance restarts.
        Queued webhooks expire after 15 minutes.
      </div>
    </div>
  );
}
