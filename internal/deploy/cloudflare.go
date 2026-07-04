package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/timothydodd/tagalong/internal/model"
)

// cfPurgeMaxURLs is Cloudflare's per-request cap for the files purge list.
const cfPurgeMaxURLs = 30

// TokenGetter returns the global Cloudflare API token (from settings).
type TokenGetter func() (string, error)

// cfAPIBase is the Cloudflare API root (overridable in tests).
const cfAPIBase = "https://api.cloudflare.com/client/v4"

// CloudflarePurger purges Cloudflare cache after a successful deploy. It
// implements the engine's Purger interface.
type CloudflarePurger struct {
	http    *http.Client
	token   TokenGetter
	baseURL string
}

// NewCloudflarePurger builds a purger that reads the API token lazily via get.
func NewCloudflarePurger(get TokenGetter) *CloudflarePurger {
	return &CloudflarePurger{
		http:    &http.Client{Timeout: 15 * time.Second},
		token:   get,
		baseURL: cfAPIBase,
	}
}

// Purge runs the app's configured cache purge. A disabled config is a no-op.
func (p *CloudflarePurger) Purge(ctx context.Context, app model.App) error {
	cfg := app.CFPurge
	if !cfg.Enabled {
		return nil
	}
	if cfg.ZoneID == "" {
		return fmt.Errorf("cloudflare purge enabled but zone_id is empty")
	}
	token, err := p.token()
	if err != nil {
		return fmt.Errorf("read cloudflare token: %w", err)
	}
	if token == "" {
		return fmt.Errorf("cloudflare API token not configured (Settings)")
	}

	if cfg.Mode == model.CFModeURLs {
		if len(cfg.URLs) == 0 {
			return fmt.Errorf("cloudflare purge mode 'urls' but no URLs configured")
		}
		// Chunk the URL list to respect the per-request cap.
		for i := 0; i < len(cfg.URLs); i += cfPurgeMaxURLs {
			end := i + cfPurgeMaxURLs
			if end > len(cfg.URLs) {
				end = len(cfg.URLs)
			}
			if err := p.post(ctx, token, cfg.ZoneID, map[string]any{"files": cfg.URLs[i:end]}); err != nil {
				return err
			}
		}
		return nil
	}
	// Default: purge everything.
	return p.post(ctx, token, cfg.ZoneID, map[string]any{"purge_everything": true})
}

func (p *CloudflarePurger) post(ctx context.Context, token, zoneID string, body map[string]any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/zones/%s/purge_cache", p.baseURL, zoneID)

	// One retry on transient 5xx.
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("cloudflare purge %s: %s", resp.Status, string(respBody))
		if resp.StatusCode < 500 {
			return lastErr // client error won't be fixed by retrying
		}
	}
	return lastErr
}
