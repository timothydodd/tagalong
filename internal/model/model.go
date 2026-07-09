// Package model holds the core domain types shared across the store, HTTP API,
// deploy engine, and poller.
package model

import (
	"encoding/json"
	"time"
)

// Tag strategy identifiers.
const (
	StrategyExact  = "exact"  // deploy any tag matching a regex pattern
	StrategyLatest = "latest" // rollout-restart when a rolling tag's digest changes
	StrategySemver = "semver" // deploy when a newer semver tag appears
	StrategyRegex  = "regex"  // alias of exact with a required pattern
)

// Workload kinds we can target.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
)

// Deploy event triggers.
const (
	TriggerDockerHub = "webhook:dockerhub"
	TriggerGitHub    = "webhook:github"
	TriggerPoll      = "poll"
	TriggerManual    = "manual"
)

// Deploy event actions.
const (
	ActionPatch   = "patch"
	ActionRestart = "restart"
	ActionSkipped = "skipped"
	ActionPurge   = "purge" // Cloudflare cache purge, recorded as its own history event
)

// Deploy event statuses.
const (
	StatusPending = "pending"
	StatusRolling = "rolling"
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusUnknown = "unknown"
)

// Cloudflare purge modes.
const (
	CFModeEverything = "everything"
	CFModeURLs       = "urls"
)

// CFDefaultDelaySeconds is how long to wait before firing the purge when an app
// leaves the delay unset (5 minutes).
const CFDefaultDelaySeconds = 300

// StrategyConf is the per-strategy configuration blob stored as JSON.
type StrategyConf struct {
	// Pattern is a regexp used by exact/regex strategies to match acceptable tags.
	Pattern string `json:"pattern,omitempty"`
	// TrackTag is the rolling tag watched by the latest strategy (default "latest").
	TrackTag string `json:"track_tag,omitempty"`
	// Constraint is an optional semver constraint (e.g. ">=0.6").
	Constraint string `json:"constraint,omitempty"`
}

// CFPurge is the per-app Cloudflare cache-purge configuration.
type CFPurge struct {
	Enabled bool     `json:"enabled"`
	ZoneID  string   `json:"zone_id,omitempty"`
	Mode    string   `json:"mode,omitempty"` // everything | urls
	URLs    []string `json:"urls,omitempty"`
	// DelaySeconds delays the purge after a successful deploy. nil means use
	// CFDefaultDelaySeconds; 0 means purge immediately.
	DelaySeconds *int `json:"delay_seconds,omitempty"`
}

// Delay returns how long to wait before firing the purge. An unset delay
// defaults to CFDefaultDelaySeconds; a negative value is treated as 0.
func (c CFPurge) Delay() time.Duration {
	secs := CFDefaultDelaySeconds
	if c.DelaySeconds != nil {
		secs = *c.DelaySeconds
	}
	if secs < 0 {
		secs = 0
	}
	return time.Duration(secs) * time.Second
}

// Target is a single k8s workload+container that an App updates.
type Target struct {
	ID        int64  `json:"id"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"` // Deployment | StatefulSet
	Name      string `json:"name"`
	Container string `json:"container"`
}

// App is a configured application tagalong can deploy.
type App struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	ImageRepo     string       `json:"image_repo"` // normalized registry/path, the webhook match key
	TagStrategy   string       `json:"tag_strategy"`
	StrategyConf  StrategyConf `json:"strategy_conf"`
	Targets       []Target     `json:"targets"`
	WebhookToken  string       `json:"webhook_token"`
	PollEnabled   bool         `json:"poll_enabled"`
	PollInterval  int          `json:"poll_interval_sec"`
	CFPurge       CFPurge      `json:"cf_purge"`
	Enabled       bool         `json:"enabled"`
	LastSeenTag   string       `json:"last_seen_tag,omitempty"`
	LastSeenDigest string      `json:"last_seen_digest,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

// DeployEvent is one row in the deploy history.
type DeployEvent struct {
	ID         int64      `json:"id"`
	AppID      *int64     `json:"app_id,omitempty"`
	AppName    string     `json:"app_name"`
	Trigger    string     `json:"trigger"`
	Action     string     `json:"action"`
	OldImage   string     `json:"old_image,omitempty"`
	NewImage   string     `json:"new_image,omitempty"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	CFPurged   bool       `json:"cf_purged"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// RegistryCred holds credentials for authenticating to a registry.
type RegistryCred struct {
	Registry string `json:"registry"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Settings are the global key/value settings surfaced to the UI.
type Settings struct {
	CloudflareAPIToken  string `json:"cloudflare_api_token"`
	GitHubWebhookSecret string `json:"github_webhook_secret"`
}

// Settings keys.
const (
	KeyCloudflareAPIToken  = "cloudflare_api_token"
	KeyGitHubWebhookSecret = "github_webhook_secret"

	// Portal auth. These are internal — never surfaced via /api/settings.
	KeyAuthUsername          = "auth_username"
	KeyAuthPasswordHash      = "auth_password_hash"
	KeyAuthSessionSecret     = "auth_session_secret"
	KeyAuthPasswordIsDefault = "auth_password_is_default"
)

// MarshalConf serializes a value to a JSON string for storage; returns "{}" on error.
func MarshalConf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
