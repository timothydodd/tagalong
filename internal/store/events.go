package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/timothydodd/tagalong/internal/model"
)

// CreateEvent inserts a new deploy event (typically in pending status) and
// returns it with populated id and timestamps.
func (s *Store) CreateEvent(e model.DeployEvent) (model.DeployEvent, error) {
	res, err := s.db.Exec(`INSERT INTO deploy_events
		(app_id, app_name, trigger, action, old_image, new_image, status, detail, cf_purged)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.AppID, e.AppName, e.Trigger, e.Action, nullStr(e.OldImage), nullStr(e.NewImage),
		e.Status, nullStr(e.Detail), boolToInt(e.CFPurged))
	if err != nil {
		return model.DeployEvent{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetEvent(id)
}

// UpdateEvent updates a mutable event's status/detail/images/cf_purged and sets
// finished_at when the status is terminal.
func (s *Store) UpdateEvent(e model.DeployEvent) error {
	terminal := e.Status == model.StatusSuccess || e.Status == model.StatusFailed ||
		e.Status == model.StatusSkipped || e.Status == model.StatusUnknown
	if terminal {
		_, err := s.db.Exec(`UPDATE deploy_events SET action = ?, old_image = ?, new_image = ?,
			status = ?, detail = ?, cf_purged = ?, finished_at = datetime('now') WHERE id = ?`,
			e.Action, nullStr(e.OldImage), nullStr(e.NewImage), e.Status,
			nullStr(e.Detail), boolToInt(e.CFPurged), e.ID)
		return err
	}
	_, err := s.db.Exec(`UPDATE deploy_events SET action = ?, old_image = ?, new_image = ?,
		status = ?, detail = ?, cf_purged = ? WHERE id = ?`,
		e.Action, nullStr(e.OldImage), nullStr(e.NewImage), e.Status,
		nullStr(e.Detail), boolToInt(e.CFPurged), e.ID)
	return err
}

// GetEvent returns a single deploy event by id.
func (s *Store) GetEvent(id int64) (model.DeployEvent, error) {
	row := s.db.QueryRow(`SELECT id, app_id, app_name, trigger, action, old_image, new_image,
		status, detail, cf_purged, started_at, finished_at FROM deploy_events WHERE id = ?`, id)
	e, err := scanEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DeployEvent{}, ErrNotFound
	}
	return e, err
}

// ListEvents returns deploy events newest-first, keyset-paginated by id. If
// appID > 0 only that app's events are returned. beforeID > 0 returns events
// with id < beforeID (for "load more"). limit defaults to 50, capped at 200.
func (s *Store) ListEvents(appID, beforeID int64, limit int) ([]model.DeployEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT id, app_id, app_name, trigger, action, old_image, new_image,
		status, detail, cf_purged, started_at, finished_at FROM deploy_events WHERE 1=1`
	var args []any
	if appID > 0 {
		q += ` AND app_id = ?`
		args = append(args, appID)
	}
	if beforeID > 0 {
		q += ` AND id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []model.DeployEvent{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListInterrupted returns events left in pending/rolling by a previous process
// (e.g. a crash, or tagalong restarting itself mid-rollout), oldest-first so
// startup reconciliation processes them in order.
func (s *Store) ListInterrupted() ([]model.DeployEvent, error) {
	rows, err := s.db.Query(`SELECT id, app_id, app_name, trigger, action, old_image, new_image,
		status, detail, cf_purged, started_at, finished_at FROM deploy_events
		WHERE status IN (?, ?) ORDER BY id ASC`, model.StatusPending, model.StatusRolling)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []model.DeployEvent{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// PruneEvents enforces history retention: terminal events older than maxAge are
// deleted, and if more than keep terminal events remain, the oldest beyond that
// count are deleted too. In-flight (pending/rolling) events are never touched.
// Returns the number of rows deleted.
func (s *Store) PruneEvents(maxAge time.Duration, keep int) (int64, error) {
	inflight := `status NOT IN ('` + model.StatusPending + `', '` + model.StatusRolling + `')`
	var total int64
	res, err := s.db.Exec(`DELETE FROM deploy_events WHERE `+inflight+
		` AND started_at < datetime('now', ?)`,
		fmt.Sprintf("-%d seconds", int64(maxAge.Seconds())))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	total += n

	res, err = s.db.Exec(`DELETE FROM deploy_events WHERE `+inflight+` AND id NOT IN (
		SELECT id FROM deploy_events WHERE `+inflight+` ORDER BY id DESC LIMIT ?)`, keep)
	if err != nil {
		return total, err
	}
	n, _ = res.RowsAffected()
	total += n
	return total, nil
}

// SweepStale marks any events left in pending/rolling (e.g. from a crash) as
// unknown. Called once at startup. Returns the number of rows updated.
func (s *Store) SweepStale() (int64, error) {
	res, err := s.db.Exec(`UPDATE deploy_events SET status = ?, detail = 'interrupted (service restart)',
		finished_at = datetime('now') WHERE status IN (?, ?)`,
		model.StatusUnknown, model.StatusPending, model.StatusRolling)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func scanEvent(sc scanner) (model.DeployEvent, error) {
	var e model.DeployEvent
	var appID sql.NullInt64
	var oldImg, newImg, detail, finishedAt sql.NullString
	var startedAt string
	var cfPurged int
	err := sc.Scan(&e.ID, &appID, &e.AppName, &e.Trigger, &e.Action, &oldImg, &newImg,
		&e.Status, &detail, &cfPurged, &startedAt, &finishedAt)
	if err != nil {
		return model.DeployEvent{}, err
	}
	if appID.Valid {
		e.AppID = &appID.Int64
	}
	e.OldImage = oldImg.String
	e.NewImage = newImg.String
	e.Detail = detail.String
	e.CFPurged = cfPurged != 0
	e.StartedAt = parseTime(startedAt)
	if finishedAt.Valid {
		t := parseTime(finishedAt.String)
		e.FinishedAt = &t
	}
	return e, nil
}
