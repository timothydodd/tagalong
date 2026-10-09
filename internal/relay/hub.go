package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/store"
)

const (
	// maxQueue caps how many undelivered messages an offline agent accumulates;
	// the oldest are dropped first. Agents can still catch up via polling.
	maxQueue = 100
	// queueTTL drops messages an agent hasn't collected in time — a deploy
	// trigger from long ago is more surprising than useful.
	queueTTL = 15 * time.Minute
	// maxResultsBody caps an agent's results POST.
	maxResultsBody = 1 << 20
)

// AgentStore is the persistence the hub needs.
type AgentStore interface {
	ListAgents() ([]model.Agent, error)
	GetAgentByToken(token string) (model.Agent, error)
}

// Hub queues relayed webhooks per agent and serves the agent long-poll API.
type Hub struct {
	store    AgentStore
	log      *slog.Logger
	pollWait time.Duration

	mu      sync.Mutex
	conns   map[int64]*conn
	waiters map[string]chan AgentResult
}

// conn is the in-memory state for one agent.
type conn struct {
	queue    []queued
	wake     chan struct{} // buffered(1): signals a waiting poll
	lastSeen time.Time
	polling  int
}

type queued struct {
	msg Message
	at  time.Time
}

// NewHub returns a hub backed by st.
func NewHub(st AgentStore, log *slog.Logger) *Hub {
	return &Hub{
		store:    st,
		log:      log,
		pollWait: PollWait,
		conns:    map[int64]*conn{},
		waiters:  map[string]chan AgentResult{},
	}
}

// conn returns (creating if needed) the state for an agent. Caller holds mu.
func (h *Hub) conn(id int64) *conn {
	c, ok := h.conns[id]
	if !ok {
		c = &conn{wake: make(chan struct{}, 1)}
		h.conns[id] = c
	}
	return c
}

// connected reports whether an agent is polling now or polled recently enough
// that it's just between polls. Caller holds mu.
func (h *Hub) connected(c *conn) bool {
	return c.polling > 0 || (!c.lastSeen.IsZero() && time.Since(c.lastSeen) < 2*h.pollWait)
}

// HasAgents reports whether any agent is registered (i.e. relaying applies).
func (h *Hub) HasAgents() bool {
	agents, err := h.store.ListAgents()
	return err == nil && len(agents) > 0
}

// Relay queues a webhook for every registered agent and waits until each
// connected agent has answered or ctx ends. Agents that didn't answer are
// reported as pending (connected) or queued (offline) — their copy is still
// delivered.
func (h *Hub) Relay(ctx context.Context, kind, token string, body []byte, verified bool) []AgentResult {
	agents, err := h.store.ListAgents()
	if err != nil {
		h.log.Warn("relay: list agents", "err", err)
		return nil
	}
	if len(agents) == 0 {
		return nil
	}

	msg := Message{ID: newID(), Kind: kind, Token: token, Body: body, Verified: verified}
	ch := make(chan AgentResult, len(agents))
	now := time.Now()

	// Only wait on agents that are connected now; an offline agent can't
	// answer, so holding the webhook request open for it is pointless.
	online := 0

	h.mu.Lock()
	h.waiters[msg.ID] = ch
	for _, a := range agents {
		c := h.conn(a.ID)
		if h.connected(c) {
			online++
		}
		c.queue = append(c.queue, queued{msg: msg, at: now})
		if len(c.queue) > maxQueue {
			c.queue = c.queue[len(c.queue)-maxQueue:]
		}
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.waiters, msg.ID)
		h.mu.Unlock()
	}()

	got := map[string]AgentResult{}
wait:
	for len(got) < online {
		select {
		case r := <-ch:
			got[r.Agent] = r
		case <-ctx.Done():
			break wait
		}
	}

	out := make([]AgentResult, 0, len(agents))
	h.mu.Lock()
	for _, a := range agents {
		if r, ok := got[a.Name]; ok {
			out = append(out, r)
			continue
		}
		state := StateQueued
		if h.connected(h.conn(a.ID)) {
			state = StatePending
		}
		out = append(out, AgentResult{Agent: a.Name, State: state})
	}
	h.mu.Unlock()
	return out
}

// Statuses overlays live connection state onto stored agents.
func (h *Hub) Statuses(agents []model.Agent) []model.Agent {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range agents {
		c, ok := h.conns[agents[i].ID]
		if !ok {
			continue
		}
		agents[i].Connected = h.connected(c)
		if !c.lastSeen.IsZero() {
			t := c.lastSeen
			agents[i].LastSeen = &t
		}
		agents[i].Queued = len(c.queue)
	}
	return agents
}

// Forget drops an agent's in-memory state (after it is deleted). A poll still
// blocked for it returns empty and its next poll fails auth.
func (h *Hub) Forget(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.conns[id]; ok {
		c.queue = nil
		delete(h.conns, id)
	}
}

// authAgent resolves the bearer token on an agent request.
func (h *Hub) authAgent(r *http.Request) (model.Agent, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return model.Agent{}, false
	}
	a, err := h.store.GetAgentByToken(tok)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			h.log.Warn("relay: agent auth", "err", err)
		}
		return model.Agent{}, false
	}
	return a, true
}

// HandlePoll serves GET /agent/v1/poll: returns queued messages for the agent,
// blocking up to pollWait when there are none.
func (h *Hub) HandlePoll(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.authAgent(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid agent token"})
		return
	}

	h.mu.Lock()
	c := h.conn(agent.ID)
	c.polling++
	c.lastSeen = time.Now()
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		c.polling--
		c.lastSeen = time.Now()
		h.mu.Unlock()
	}()

	// Send headers now so the agent knows it's connected without waiting out
	// the long-poll; the batch follows as the body.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	timer := time.NewTimer(h.pollWait)
	defer timer.Stop()
	for {
		if batch := h.take(c); len(batch) > 0 {
			json.NewEncoder(w).Encode(batch)
			return
		}
		select {
		case <-c.wake:
		case <-timer.C:
			json.NewEncoder(w).Encode([]Message{})
			return
		case <-r.Context().Done():
			return
		}
	}
}

// take removes and returns the agent's unexpired queued messages.
func (h *Hub) take(c *conn) []Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Message
	for _, q := range c.queue {
		if time.Since(q.at) <= queueTTL {
			out = append(out, q.msg)
		}
	}
	c.queue = nil
	return out
}

// HandleResults serves POST /agent/v1/results: hands each outcome to the
// webhook request waiting on it (late results are logged and dropped).
func (h *Hub) HandleResults(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.authAgent(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid agent token"})
		return
	}
	var results []Result
	if err := json.NewDecoder(io.LimitReader(r.Body, maxResultsBody)).Decode(&results); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	h.mu.Lock()
	h.conn(agent.ID).lastSeen = time.Now()
	for _, res := range results {
		h.log.Info("relay result", "agent", agent.Name, "id", res.ID, "status", res.Status, "response", string(res.Body))
		ch, ok := h.waiters[res.ID]
		if !ok {
			continue // the webhook caller already got its answer
		}
		select {
		case ch <- AgentResult{Agent: agent.Name, State: StateDone, Status: res.Status, Response: res.Body}:
		default:
		}
	}
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
