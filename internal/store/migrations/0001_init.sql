CREATE TABLE apps (
  id                INTEGER PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  image_repo        TEXT NOT NULL,
  tag_strategy      TEXT NOT NULL,
  strategy_conf     TEXT NOT NULL DEFAULT '{}',
  webhook_token     TEXT NOT NULL,
  poll_enabled      INTEGER NOT NULL DEFAULT 0,
  poll_interval_sec INTEGER NOT NULL DEFAULT 300,
  cf_purge          TEXT NOT NULL DEFAULT '{}',
  enabled           INTEGER NOT NULL DEFAULT 1,
  last_seen_tag     TEXT,
  last_seen_digest  TEXT,
  created_at        TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_apps_image_repo ON apps(image_repo);
CREATE INDEX idx_apps_webhook_token ON apps(webhook_token);

CREATE TABLE app_targets (
  id        INTEGER PRIMARY KEY,
  app_id    INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  namespace TEXT NOT NULL,
  kind      TEXT NOT NULL DEFAULT 'Deployment',
  name      TEXT NOT NULL,
  container TEXT NOT NULL,
  UNIQUE(app_id, namespace, kind, name, container)
);
CREATE INDEX idx_targets_app ON app_targets(app_id);

CREATE TABLE deploy_events (
  id          INTEGER PRIMARY KEY,
  app_id      INTEGER REFERENCES apps(id) ON DELETE SET NULL,
  app_name    TEXT NOT NULL,
  trigger     TEXT NOT NULL,
  action      TEXT NOT NULL,
  old_image   TEXT,
  new_image   TEXT,
  status      TEXT NOT NULL,
  detail      TEXT,
  cf_purged   INTEGER NOT NULL DEFAULT 0,
  started_at  TEXT NOT NULL DEFAULT (datetime('now')),
  finished_at TEXT
);
CREATE INDEX idx_events_app ON deploy_events(app_id, id DESC);
CREATE INDEX idx_events_id ON deploy_events(id DESC);

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE registry_creds (
  registry TEXT PRIMARY KEY,
  username TEXT NOT NULL,
  password TEXT NOT NULL
);
