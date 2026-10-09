-- Agents are downstream tagalong instances that connect OUT to this one (the
-- hub) and receive webhooks the hub has no app for. Only a hash of each
-- agent's token is stored; the plaintext is shown once at creation.
CREATE TABLE agents (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
