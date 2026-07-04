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

export interface Settings {
  cloudflare_api_token: string;
  github_webhook_secret: string;
}

export interface RegistryCred {
  registry: string;
  username: string;
  password: string;
}

async function failIfNotOk(res: Response): Promise<void> {
  if (res.ok) return;
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
  await failIfNotOk(res);
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

// reqText fetches a plain-text (YAML) body.
async function reqText(method: string, path: string): Promise<string> {
  const res = await fetch(path, { method });
  await failIfNotOk(res);
  return res.text();
}

// reqYAML sends a raw YAML body and parses the JSON response.
async function reqYAML<T>(method: string, path: string, yamlText: string): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/x-yaml" },
    body: yamlText,
  });
  await failIfNotOk(res);
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
  getSettings: () => req<Settings>("GET", "/api/settings"),
  putSettings: (s: Settings) => req<Settings>("PUT", "/api/settings", s),
  listRegistries: () =>
    req<RegistryCred[]>("GET", "/api/settings/registries").then((r) => r ?? []),
  putRegistry: (c: RegistryCred) => req<void>("PUT", "/api/settings/registries", c),
  deleteRegistry: (registry: string) =>
    req<void>("DELETE", `/api/settings/registries/${encodeURIComponent(registry)}`),
};
