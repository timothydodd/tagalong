// Typed client for the tagalong REST API. Types mirror internal/model in the Go
// backend.

export type TagStrategy = "exact" | "latest" | "semver" | "regex";
export type WorkloadKind = "Deployment" | "StatefulSet";

export interface StrategyConf {
  pattern?: string;
  track_tag?: string;
  constraint?: string;
}

export interface CFPurge {
  enabled: boolean;
  zone_id?: string;
  mode?: "everything" | "urls";
  urls?: string[];
  // Delay before the purge fires after a successful deploy. Omitted defaults to
  // 300s (5 min) on the backend; 0 means purge immediately.
  delay_seconds?: number;
}

export interface Target {
  id?: number;
  namespace: string;
  kind: WorkloadKind;
  name: string;
  container: string;
}

export interface App {
  id: number;
  name: string;
  image_repo: string;
  tag_strategy: TagStrategy;
  strategy_conf: StrategyConf;
  targets: Target[];
  webhook_token: string;
  poll_enabled: boolean;
  poll_interval_sec: number;
  cf_purge: CFPurge;
  enabled: boolean;
  last_seen_tag?: string;
  last_seen_digest?: string;
  created_at: string;
  updated_at: string;
}

export type DeployStatus =
  | "pending"
  | "rolling"
  | "success"
  | "failed"
  | "skipped"
  | "unknown";

export interface DeployEvent {
  id: number;
  app_id?: number;
  app_name: string;
  trigger: string;
  action: string;
  old_image?: string;
  new_image?: string;
  status: DeployStatus;
  detail?: string;
  cf_purged: boolean;
  started_at: string;
  finished_at?: string;
}

export interface TargetStatus {
  target: Target;
  current_image: string;
  ready_replicas: number;
  replicas: number;
  available: boolean;
  error?: string;
}

export interface WorkloadContainer {
  name: string;
  image: string;
}

export interface Workload {
  namespace: string;
  kind: WorkloadKind;
  name: string;
  containers: WorkloadContainer[];
}

export interface Settings {
  cloudflare_api_token: string;
  github_webhook_secret: string;
  public_base_url: string;
}

// webhookBase returns the base URL for webhook links: the configured public base
// URL when set, otherwise the browser's current origin. Trailing slashes trimmed.
export function webhookBase(configured?: string): string {
  const c = (configured ?? "").trim().replace(/\/+$/, "");
  return c || window.location.origin;
}

export interface RegistryCred {
  registry: string;
  username: string;
  password: string;
}

export interface Me {
  username: string;
  must_change_password: boolean;
}

// onUnauthorized is invoked when a data request returns 401 (session expired or
// missing), so the app can drop back to the login screen. The login/me probes
// are exempt — a 401 there is an expected answer, not a session drop.
let onUnauthorized: (() => void) | null = null;
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}
const authProbes = new Set(["/api/login", "/api/me"]);

async function failIfNotOk(res: Response, path: string): Promise<void> {
  if (res.ok) return;
  if (res.status === 401 && !authProbes.has(path)) {
    onUnauthorized?.();
  }
  let msg = res.statusText;
  try {
    const j = await res.json();
    if (j.error) msg = j.error;
  } catch {
    /* ignore */
  }
  throw new Error(msg);
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  await failIfNotOk(res, path);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

// reqText fetches a plain-text (YAML) body.
async function reqText(method: string, path: string): Promise<string> {
  const res = await fetch(path, { method });
  await failIfNotOk(res, path);
  return res.text();
}

// reqYAML sends a raw YAML body and parses the JSON response.
async function reqYAML<T>(method: string, path: string, yamlText: string): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/x-yaml" },
    body: yamlText,
  });
  await failIfNotOk(res, path);
  const text = await res.text();
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export interface ImportResult {
  created: number;
  updated: number;
  apps: string[];
}

export const api = {
  // List endpoints coerce a null body to [] so the UI never reads .length/.map
  // of null (Go marshals an empty slice as JSON null).
  listApps: () => req<App[]>("GET", "/api/apps").then((a) => a ?? []),
  getApp: (id: number) => req<App>("GET", `/api/apps/${id}`),
  createApp: (a: Partial<App>) => req<App>("POST", "/api/apps", a),
  updateApp: (id: number, a: Partial<App>) => req<App>("PUT", `/api/apps/${id}`, a),
  deleteApp: (id: number) => req<void>("DELETE", `/api/apps/${id}`),
  exportApps: () => reqText("GET", "/api/apps/export"),
  exportApp: (id: number) => reqText("GET", `/api/apps/${id}/export`),
  importApps: (yamlText: string) => reqYAML<ImportResult>("POST", "/api/apps/import", yamlText),
  updateAppYAML: (id: number, yamlText: string) =>
    reqYAML<App>("PUT", `/api/apps/${id}/yaml`, yamlText),
  deploy: (id: number, tag?: string) =>
    req<DeployEvent>("POST", `/api/apps/${id}/deploy`, { tag: tag ?? "" }),
  rotateToken: (id: number) =>
    req<{ webhook_token: string }>("POST", `/api/apps/${id}/token/rotate`),
  listWorkloads: () => req<Workload[]>("GET", "/api/workloads").then((w) => w ?? []),
  appStatus: (id: number) => req<TargetStatus[]>("GET", `/api/apps/${id}/status`).then((s) => s ?? []),
  appTags: (id: number) => req<string[]>("GET", `/api/apps/${id}/tags`).then((t) => t ?? []),
  listEvents: (params?: { app_id?: number; before_id?: number; limit?: number }) => {
    const q = new URLSearchParams();
    if (params?.app_id) q.set("app_id", String(params.app_id));
    if (params?.before_id) q.set("before_id", String(params.before_id));
    if (params?.limit) q.set("limit", String(params.limit));
    const qs = q.toString();
    return req<DeployEvent[]>("GET", `/api/events${qs ? "?" + qs : ""}`).then((e) => e ?? []);
  },
  me: () => req<Me>("GET", "/api/me"),
  login: (username: string, password: string) =>
    req<Me>("POST", "/api/login", { username, password }),
  logout: () => req<void>("POST", "/api/logout"),
  changePassword: (current_password: string, new_password: string) =>
    req<Me>("POST", "/api/account/password", { current_password, new_password }),
  getSettings: () => req<Settings>("GET", "/api/settings"),
  putSettings: (s: Settings) => req<Settings>("PUT", "/api/settings", s),
  listRegistries: () =>
    req<RegistryCred[]>("GET", "/api/settings/registries").then((r) => r ?? []),
  putRegistry: (c: RegistryCred) => req<void>("PUT", "/api/settings/registries", c),
  deleteRegistry: (registry: string) =>
    req<void>("DELETE", `/api/settings/registries/${encodeURIComponent(registry)}`),
};
