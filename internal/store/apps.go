package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/timothydodd/tagalong/internal/model"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

const sqlTimeLayout = "2006-01-02 15:04:05"

func parseTime(s string) time.Time {
	// SQLite datetime('now') yields "YYYY-MM-DD HH:MM:SS" in UTC.
	if t, err := time.Parse(sqlTimeLayout, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// GenerateToken returns a random 32-hex-character token for webhook URLs.
func GenerateToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ListApps returns all apps with their targets, ordered by name.
func (s *Store) ListApps() ([]model.App, error) {
	rows, err := s.db.Query(`SELECT id, name, image_repo, tag_strategy, strategy_conf, webhook_token,
		poll_enabled, poll_interval_sec, cf_purge, enabled, last_seen_tag, last_seen_digest,
		created_at, updated_at FROM apps ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []model.App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range apps {
		targets, err := s.targetsFor(apps[i].ID)
		if err != nil {
			return nil, err
		}
		apps[i].Targets = targets
	}
	return apps, nil
}

// GetApp returns a single app by id, including its targets.
func (s *Store) GetApp(id int64) (model.App, error) {
	row := s.db.QueryRow(`SELECT id, name, image_repo, tag_strategy, strategy_conf, webhook_token,
		poll_enabled, poll_interval_sec, cf_purge, enabled, last_seen_tag, last_seen_digest,
		created_at, updated_at FROM apps WHERE id = ?`, id)
	app, err := scanApp(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.App{}, ErrNotFound
	}
	if err != nil {
		return model.App{}, err
	}
	targets, err := s.targetsFor(id)
	if err != nil {
		return model.App{}, err
	}
	app.Targets = targets
	return app, nil
}

// GetAppByToken returns the app owning the given webhook token.
func (s *Store) GetAppByToken(token string) (model.App, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM apps WHERE webhook_token = ?`, token).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.App{}, ErrNotFound
	}
	if err != nil {
		return model.App{}, err
	}
	return s.GetApp(id)
}

// GetAppByRepo returns the app whose normalized image_repo matches repo.
func (s *Store) GetAppByRepo(repo string) (model.App, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM apps WHERE image_repo = ? LIMIT 1`, repo).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.App{}, ErrNotFound
	}
	if err != nil {
		return model.App{}, err
	}
	return s.GetApp(id)
}

// CreateApp inserts a new app and its targets. If app.WebhookToken is empty one
// is generated. Returns the created app with populated id/timestamps.
func (s *Store) CreateApp(app model.App) (model.App, error) {
	if app.WebhookToken == "" {
		app.WebhookToken = GenerateToken()
	}
	if app.PollInterval <= 0 {
		app.PollInterval = 300
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.App{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO apps
		(name, image_repo, tag_strategy, strategy_conf, webhook_token, poll_enabled,
		 poll_interval_sec, cf_purge, enabled, last_seen_tag, last_seen_digest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		app.Name, app.ImageRepo, app.TagStrategy, model.MarshalConf(app.StrategyConf),
		app.WebhookToken, boolToInt(app.PollEnabled), app.PollInterval,
		model.MarshalConf(app.CFPurge), boolToInt(app.Enabled),
		nullStr(app.LastSeenTag), nullStr(app.LastSeenDigest))
	if err != nil {
		return model.App{}, err
	}
	id, _ := res.LastInsertId()
	if err := replaceTargets(tx, id, app.Targets); err != nil {
		return model.App{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.App{}, err
	}
	return s.GetApp(id)
}

// UpdateApp updates an existing app's mutable fields and replaces its targets.
func (s *Store) UpdateApp(app model.App) (model.App, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.App{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE apps SET
		name = ?, image_repo = ?, tag_strategy = ?, strategy_conf = ?,
		poll_enabled = ?, poll_interval_sec = ?, cf_purge = ?, enabled = ?,
		updated_at = datetime('now')
		WHERE id = ?`,
		app.Name, app.ImageRepo, app.TagStrategy, model.MarshalConf(app.StrategyConf),
		boolToInt(app.PollEnabled), app.PollInterval, model.MarshalConf(app.CFPurge),
		boolToInt(app.Enabled), app.ID)
	if err != nil {
		return model.App{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.App{}, ErrNotFound
	}
	if err := replaceTargets(tx, app.ID, app.Targets); err != nil {
		return model.App{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.App{}, err
	}
	return s.GetApp(app.ID)
}

// DeleteApp removes an app and (via cascade) its targets.
func (s *Store) DeleteApp(id int64) error {
	res, err := s.db.Exec(`DELETE FROM apps WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateToken assigns a fresh webhook token and returns it.
func (s *Store) RotateToken(id int64) (string, error) {
	token := GenerateToken()
	res, err := s.db.Exec(`UPDATE apps SET webhook_token = ?, updated_at = datetime('now') WHERE id = ?`, token, id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

// SetLastSeen records the tag/digest most recently acted upon for an app.
func (s *Store) SetLastSeen(id int64, tag, digest string) error {
	_, err := s.db.Exec(`UPDATE apps SET last_seen_tag = ?, last_seen_digest = ?, updated_at = datetime('now') WHERE id = ?`,
		nullStr(tag), nullStr(digest), id)
	return err
}

func (s *Store) targetsFor(appID int64) ([]model.Target, error) {
	rows, err := s.db.Query(`SELECT id, namespace, kind, name, container FROM app_targets WHERE app_id = ? ORDER BY id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []model.Target
	for rows.Next() {
		var t model.Target
		if err := rows.Scan(&t.ID, &t.Namespace, &t.Kind, &t.Name, &t.Container); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func replaceTargets(tx *sql.Tx, appID int64, targets []model.Target) error {
	if _, err := tx.Exec(`DELETE FROM app_targets WHERE app_id = ?`, appID); err != nil {
		return err
	}
	for _, t := range targets {
		kind := t.Kind
		if kind == "" {
			kind = model.KindDeployment
		}
		if _, err := tx.Exec(`INSERT INTO app_targets (app_id, namespace, kind, name, container) VALUES (?, ?, ?, ?, ?)`,
			appID, t.Namespace, kind, t.Name, t.Container); err != nil {
			return fmt.Errorf("insert target %s/%s: %w", t.Namespace, t.Name, err)
		}
	}
	return nil
}

// scanner abstracts *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanApp(sc scanner) (model.App, error) {
	var app model.App
	var confJSON, cfJSON string
	var lastTag, lastDigest sql.NullString
	var pollEnabled, enabled int
	var createdAt, updatedAt string
	err := sc.Scan(&app.ID, &app.Name, &app.ImageRepo, &app.TagStrategy, &confJSON,
		&app.WebhookToken, &pollEnabled, &app.PollInterval, &cfJSON, &enabled,
		&lastTag, &lastDigest, &createdAt, &updatedAt)
	if err != nil {
		return model.App{}, err
	}
	json.Unmarshal([]byte(confJSON), &app.StrategyConf)
	json.Unmarshal([]byte(cfJSON), &app.CFPurge)
	app.PollEnabled = pollEnabled != 0
	app.Enabled = enabled != 0
	app.LastSeenTag = lastTag.String
	app.LastSeenDigest = lastDigest.String
	app.CreatedAt = parseTime(createdAt)
	app.UpdatedAt = parseTime(updatedAt)
	return app, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
