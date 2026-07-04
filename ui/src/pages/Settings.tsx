import { useEffect, useState } from "react";
import { api, type RegistryCred, type Settings as SettingsT } from "../api";
import { ErrorBox } from "../components";

export default function Settings() {
  const [settings, setSettings] = useState<SettingsT>({
    cloudflare_api_token: "",
    github_webhook_secret: "",
  });
  const [creds, setCreds] = useState<RegistryCred[]>([]);
  const [newCred, setNewCred] = useState<RegistryCred>({ registry: "", username: "", password: "" });
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const load = () => {
    api.getSettings().then(setSettings).catch((e) => setError(String(e)));
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
      setError(String(e));
    }
  };

  const addCred = async () => {
    if (!newCred.registry) return;
    try {
      await api.putRegistry(newCred);
      setNewCred({ registry: "", username: "", password: "" });
      load();
    } catch (e) {
      setError(String(e));
    }
  };

  const delCred = async (registry: string) => {
    await api.deleteRegistry(registry);
    load();
  };

  return (
    <>
      <div className="page-head">
        <h1>Settings</h1>
      </div>
      <ErrorBox error={error} />

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
            type="password"
            value={settings.github_webhook_secret}
            onChange={(e) => setSettings({ ...settings, github_webhook_secret: e.target.value })}
            placeholder="(unset)"
          />
          <div className="hint">
            Validates <code>X-Hub-Signature-256</code> on <code>/hooks/github</code>. Set the same
            value in the GitHub webhook config.
          </div>
        </div>
        <button className="btn primary" onClick={saveSettings}>
          {saved ? "Saved ✓" : "Save settings"}
        </button>
        <div className="hint" style={{ marginTop: 8 }}>
          Stored values are masked as <code>********</code>; leave a field masked to keep it
          unchanged.
        </div>
      </div>

      <div className="card">
        <div className="section-title">Registry credentials</div>
        <div className="hint" style={{ marginTop: -6, marginBottom: 14 }}>
          Used for polling private registries (e.g. <code>reg.dodd.rocks</code>) and higher Docker
          Hub rate limits. Public repos need no credentials.
        </div>
        {creds.length > 0 && (
          <table style={{ marginBottom: 14 }}>
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
        )}
        <div className="row-4" style={{ gridTemplateColumns: "1.2fr 1fr 1fr auto" }}>
          <input
            type="text"
            placeholder="reg.dodd.rocks"
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
