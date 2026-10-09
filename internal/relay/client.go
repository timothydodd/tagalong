package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/timothydodd/tagalong/internal/model"
)

// Handler processes one relayed webhook on the agent and returns the HTTP
// status and JSON body its local webhook receiver would have produced.
type Handler func(ctx context.Context, m Message) (status int, body []byte)

// Client is the agent side: it long-polls the hub and runs each relayed
// webhook through the local receivers.
type Client struct {
	base   string
	token  string
	handle Handler
	log    *slog.Logger
	poll   *http.Client
	post   *http.Client

	mu     sync.Mutex
	status model.HubStatus
}

// NewClient returns an agent client for the hub at hubURL (its public origin,
// e.g. https://tagalong.example.com).
func NewClient(hubURL, token string, h Handler, log *slog.Logger) *Client {
	base := strings.TrimRight(strings.TrimSpace(hubURL), "/")
	return &Client{
		base:   base,
		token:  token,
		handle: h,
		log:    log,
		poll:   &http.Client{Timeout: PollWait + 20*time.Second},
		post:   &http.Client{Timeout: 15 * time.Second},
		status: model.HubStatus{Enabled: true, URL: base},
	}
}

// Status returns a snapshot of the connection state for the UI.
func (c *Client) Status() model.HubStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Run polls until ctx is cancelled, backing off on errors.
func (c *Client) Run(ctx context.Context) {
	c.log.Info("agent mode: connecting to hub", "hub", c.base)
	backoff := time.Second
	for ctx.Err() == nil {
		msgs, err := c.pollOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.setErr(err)
			c.log.Warn("hub poll failed", "hub", c.base, "err", err, "retry_in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		c.addRelayed(len(msgs))
		for _, m := range msgs {
			go c.process(ctx, m)
		}
	}
}

func (c *Client) pollOnce(ctx context.Context) ([]Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+PathPoll, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.poll.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("hub returned %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	// The hub sends headers as soon as the poll is accepted, so we're
	// connected now even though the batch may take PollWait to arrive.
	c.setConnected()
	var msgs []Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, fmt.Errorf("decode poll response: %w", err)
	}
	return msgs, nil
}

// process runs one relayed webhook locally and reports the outcome to the hub.
func (c *Client) process(ctx context.Context, m Message) {
	status, body := c.handle(ctx, m)
	c.log.Info("relayed webhook processed", "id", m.ID, "kind", m.Kind, "status", status)

	if !json.Valid(body) {
		body, _ = json.Marshal(map[string]string{"raw": string(body)})
	}
	payload, _ := json.Marshal([]Result{{ID: m.ID, Status: status, Body: body}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+PathResults, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.post.Do(req)
	if err != nil {
		c.log.Warn("report relay result", "id", m.ID, "err", err)
		return
	}
	resp.Body.Close()
}

func (c *Client) setErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Connected = false
	c.status.LastError = err.Error()
}

func (c *Client) setConnected() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.status.Connected = true
	c.status.LastContact = &now
	c.status.LastError = ""
}

func (c *Client) addRelayed(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.status.LastContact = &now
	c.status.Relayed += int64(n)
}
