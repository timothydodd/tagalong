import { useEffect, useState } from "react";
import { api, webhookBase, type RegistryCred, type Settings as SettingsT } from "../api";
import { CopyField, errMsg, ErrorBox } from "../components";
import { useAuth } from "../auth";

function AccountCard() {
  const { user, refresh } = useAuth();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const change = async () => {
    setError(null);
    if (next !== confirm) {
      setError("New password and confirmation do not match.");
      return;
    }
    if (next.length < 8) {
      setError("New password must be at least 8 characters.");
      return;
    }
    try {
      await api.changePassword(current, next);
      setCurrent("");
      setNext("");
      setConfirm("");
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      await refresh();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  return (
    <div className="card">
      <div className="section-title">Account</div>
      <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
        Signed in as <code>{user?.username}</code>. Change the portal password below.
      </div>
      <ErrorBox error={error} />
      <div className="form-row">
        <label>Current password</label>
        <input
          type="password"
          value={current}
          autoComplete="current-password"
          onChange={(e) => setCurrent(e.target.value)}
        />
      </div>
      <div className="row-2">
        <div className="form-row">
          <label>New password</label>
          <input
            type="password"
            value={next}
            autoComplete="new-password"
            onChange={(e) => setNext(e.target.value)}
          />
        </div>
        <div className="form-row">
          <label>Confirm new password</label>
          <input
            type="password"
            value={confirm}
            autoComplete="new-password"
            onChange={(e) => setConfirm(e.target.value)}
          />
        </div>
      </div>
      <button className="btn primary" onClick={change} disabled={!current || !next}>
        {saved ? "Changed ✓" : "Change password"}
      </button>
      <div className="hint" style={{ marginTop: 8 }}>
        Minimum 8 characters. You stay signed in after changing it.
      </div>
    </div>
  );
}

export default function Settings() {
  const [settings, setSettings] = useState<SettingsT>({
    cloudflare_api_token: "",
    github_webhook_secret: "",
    public_base_url: "",
  });
  const [creds, setCreds] = useState<RegistryCred[]>([]);
  const [newCred, setNewCred] = useState<RegistryCred>({ registry: "", username: "", password: "" });
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const load = () => {
    api.getSettings().then(setSettings).catch((e) => setError(errMsg(e)));
    api.listRegistries().then(setCreds).catch(() => {});
  };
  useEffect(load, []);

  const saveSettings = async () => {
    setError(null);
    try {
      const s = await api.putSettings(settings);
      setSettings(s);
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const addCred = async () => {
    if (!newCred.registry) return;
    try {
      await api.putRegistry(newCred);
      setNewCred({ registry: "", username: "", password: "" });
      load();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const delCred = async (registry: string) => {
    if (!confirm(`Remove the stored credential for "${registry}"?`)) return;
    try {
      await api.deleteRegistry(registry);
      load();
    } catch (e) {
      setError(`Remove credential: ${errMsg(e)}`);
    }
  };

  return (
    <>
      <div className="page-head">
        <h1>Settings</h1>
      </div>
      <ErrorBox error={error} />

      <AccountCard />

      <div className="card">
        <div className="section-title">Secrets</div>
        <div className="form-row">
          <label>Cloudflare API token</label>
          <input
            type="password"
            value={settings.cloudflare_api_token}
            onChange={(e) => setSettings({ ...settings, cloudflare_api_token: e.target.value })}
            placeholder="(unset)"
          />
          <div className="hint">
            Needs the <code>Zone → Cache Purge</code> permission. Used by apps with Cloudflare
            purge enabled.
          </div>
        </div>
        <div className="form-row">
          <label>GitHub webhook secret</label>
          <input
            type="text"
            value={settings.github_webhook_secret}
            onChange={(e) => setSettings({ ...settings, github_webhook_secret: e.target.value })}
            placeholder="(unset)"
          />
          <div className="hint">
            Validates <code>X-Hub-Signature-256</code> on <code>/hooks/github</code>. Shown in
            full so you can paste the same value into the GitHub webhook config.
          </div>
        </div>
        <div className="form-row">
          <label>Public base URL</label>
          <input
            type="text"
            value={settings.public_base_url}
            onChange={(e) => setSettings({ ...settings, public_base_url: e.target.value })}
            placeholder={window.location.origin}
          />
          <div className="hint">
            The externally reachable address of this portal (e.g.{" "}
            <code>https://tagalong.example.com</code>). Used to build the webhook URLs shown
            here and on each app. Leave blank to use the address in your browser.
          </div>
        </div>
        <div className="form-row">
          <label>GitHub webhook payload URL</label>
          <CopyField value={`${webhookBase(settings.public_base_url)}/hooks/github`} />
          <div className="hint">
            One URL for all apps — GitHub matches each by its <code>image_repo</code>. Content
            type <code>application/json</code>, event <code>Packages</code>.
          </div>
        </div>
        <button className="btn primary" onClick={saveSettings}>
          {saved ? "Saved ✓" : "Save settings"}
        </button>
        <div className="hint" style={{ marginTop: 8 }}>
          The Cloudflare token is masked as <code>********</code> — leave it masked to keep it
          unchanged.
        </div>
      </div>

      <div className="card">
        <div className="section-title">Registry credentials</div>
        <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
          Used for polling private registries (e.g. <code>registry.example.com</code>) and higher Docker
          Hub rate limits. Public repos need no credentials.
        </div>
        {creds.length > 0 && (
          <div className="table-wrap" style={{ marginBottom: 14 }}>
          <table>
            <thead>
              <tr>
                <th>Registry</th>
                <th>Username</th>
                <th>Password</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <tr key={c.registry}>
                  <td className="mono">{c.registry}</td>
                  <td>{c.username}</td>
                  <td className="faint">{c.password}</td>
                  <td className="right">
                    <button className="btn sm danger" onClick={() => delCred(c.registry)}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
        <div className="row-4" style={{ gridTemplateColumns: "1.2fr 1fr 1fr auto" }}>
          <input
            type="text"
            placeholder="registry.example.com"
            value={newCred.registry}
            onChange={(e) => setNewCred({ ...newCred, registry: e.target.value })}
          />
          <input
            type="text"
            placeholder="username"
            value={newCred.username}
            onChange={(e) => setNewCred({ ...newCred, username: e.target.value })}
          />
          <input
            type="password"
            placeholder="password / token"
            value={newCred.password}
            onChange={(e) => setNewCred({ ...newCred, password: e.target.value })}
          />
          <button className="btn" onClick={addCred}>
            Add
          </button>
        </div>
      </div>
    </>
  );
}
