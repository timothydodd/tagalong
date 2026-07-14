import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import { api, setUnauthorizedHandler, type Me } from "./api";

interface AuthState {
  user: Me | null;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

// useAuth exposes the current session and logout/refresh to any component
// rendered inside AuthGate.
export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthGate");
  return ctx;
}

type Status = "loading" | "anon" | "authed";

// AuthGate blocks the app behind a login screen until a session is established.
export function AuthGate({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");
  const [user, setUser] = useState<Me | null>(null);

  const refresh = async () => {
    try {
      const me = await api.me();
      setUser(me);
      setStatus("authed");
    } catch {
      setUser(null);
      setStatus("anon");
    }
  };

  useEffect(() => {
    // A 401 from any data call drops us back to the login screen.
    setUnauthorizedHandler(() => {
      setUser(null);
      setStatus("anon");
    });
    refresh();
  }, []);

  const logout = async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
      setStatus("anon");
    }
  };

  if (status === "loading") {
    return <div className="auth-screen pulse">Loading…</div>;
  }
  if (status === "anon") {
    return <Login onSuccess={refresh} />;
  }
  return (
    <AuthContext.Provider value={{ user, refresh, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

function Login({ onSuccess }: { onSuccess: () => Promise<void> }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.login(username, password);
      await onSuccess();
    } catch (err) {
      setError(err instanceof Error ? err.message : "login failed");
      setBusy(false);
    }
  };

  return (
    <div className="auth-screen">
      <form className="auth-card card" onSubmit={submit}>
        <div className="brand">
          <img src="/icon-192.png" alt="tagalong" className="brand-logo" />
        </div>
        <div className="form-row">
          <label>Username</label>
          <input
            type="text"
            value={username}
            autoFocus
            autoComplete="username"
            onChange={(e) => setUsername(e.target.value)}
          />
        </div>
        <div className="form-row">
          <label>Password</label>
          <input
            type="password"
            value={password}
            autoComplete="current-password"
            onChange={(e) => setPassword(e.target.value)}
          />
        </div>
        {error && <div className="error-box">{error}</div>}
        <button className="btn primary" type="submit" disabled={busy}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
