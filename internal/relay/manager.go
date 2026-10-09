package relay

import (
	"context"
	"log/slog"
	"sync"

	"github.com/timothydodd/tagalong/internal/model"
)

// Manager owns this instance's (optional) agent connection to a hub, so it can
// be reconfigured at runtime from the UI without a restart.
type Manager struct {
	parent  context.Context
	handler Handler
	log     *slog.Logger

	mu     sync.Mutex
	client *Client
	cancel context.CancelFunc
	locked bool
}

// NewManager returns a manager whose connections live until parent ends.
// handler runs each relayed webhook locally.
func NewManager(parent context.Context, handler Handler, log *slog.Logger) *Manager {
	return &Manager{parent: parent, handler: handler, log: log}
}

// Lock marks the connection as managed by environment variables, so the UI
// shows it read-only.
func (m *Manager) Lock() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.locked = true
}

// Locked reports whether the connection is managed by environment variables.
func (m *Manager) Locked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.locked
}

// Apply (re)connects to the hub at url with token, replacing any existing
// connection. An empty url disconnects.
func (m *Manager) Apply(url, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel, m.client = nil, nil
	}
	if url == "" {
		m.log.Info("agent mode: disconnected from hub")
		return
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.client = NewClient(url, token, m.handler, m.log)
	m.cancel = cancel
	go m.client.Run(ctx)
}

// Status reports the current connection ({enabled: false} when not an agent).
func (m *Manager) Status() model.HubStatus {
	m.mu.Lock()
	c, locked := m.client, m.locked
	m.mu.Unlock()
	st := model.HubStatus{}
	if c != nil {
		st = c.Status()
	}
	st.Locked = locked
	return st
}
