// Package relay lets one tagalong instance (the hub) forward webhooks to other
// instances (agents) that cannot accept inbound connections.
//
// Agents dial OUT to the hub and long-poll for work, so an agent on a private
// network never needs to be exposed. Every webhook the hub receives is handled
// by the hub's own apps (if any) AND queued for every registered agent; each
// agent processes it as if it had received it directly and posts the outcome
// back. Agents keep their own apps, polling, and Cloudflare purges — the hub
// only relays.
//
// Wire protocol (all requests carry "Authorization: Bearer <agent token>"):
//
//	GET  /agent/v1/poll     → 200 [Message...]  (blocks up to PollWait; [] on timeout)
//	POST /agent/v1/results  ← [Result...]       → 204
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Message kinds — which webhook receiver the agent should run.
const (
	KindDockerHub = "dockerhub"
	KindGitHub    = "github"
)

// Agent result states as reported by the hub to the webhook caller.
const (
	StateDone    = "done"    // the agent processed it and answered in time
	StatePending = "pending" // agent connected but didn't answer within the wait
	StateQueued  = "queued"  // agent offline; delivered when it next connects
)

// PollWait is how long a poll blocks before returning an empty batch. It stays
// well under the idle timeouts of Cloudflare (100s) and nginx (60s default).
const PollWait = 25 * time.Second

// Paths the hub serves for agents.
const (
	PathPoll    = "/agent/v1/poll"
	PathResults = "/agent/v1/results"
)

// Message is one relayed webhook.
type Message struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Token is the Docker Hub per-app token from the hook URL path.
	Token string `json:"token,omitempty"`
	// Verified means the hub recognized Token (so the caller is authenticated)
	// and the agent may match its own app by payload repo instead — the same
	// app on hub and agent has a different token on each.
	Verified bool `json:"verified,omitempty"`
	// Body is the raw webhook payload. For GitHub the hub has already verified
	// the signature, so agents trust relayed GitHub payloads.
	Body []byte `json:"body"`
}

// Result is an agent's outcome for one Message: the HTTP status and JSON body
// its webhook receiver would have returned.
type Result struct {
	ID     string          `json:"id"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// AgentResult is one agent's outcome as reported back to the webhook caller.
type AgentResult struct {
	Agent    string          `json:"agent"`
	State    string          `json:"state"`
	Status   int             `json:"status,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
