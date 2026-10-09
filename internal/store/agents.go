package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/timothydodd/tagalong/internal/model"
)

// HashAgentToken returns the stored form of an agent token. Tokens are long
// random values, so a plain SHA-256 is sufficient (no salt/stretching needed).
func HashAgentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ListAgents returns all registered agents ordered by name.
func (s *Store) ListAgents() ([]model.Agent, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []model.Agent{}
	for rows.Next() {
		var a model.Agent
		var created string
		if err := rows.Scan(&a.ID, &a.Name, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// CreateAgent registers an agent with the given name and plaintext token
// (only its hash is persisted).
func (s *Store) CreateAgent(name, token string) (model.Agent, error) {
	res, err := s.db.Exec(`INSERT INTO agents (name, token_hash) VALUES (?, ?)`, name, HashAgentToken(token))
	if err != nil {
		return model.Agent{}, err
	}
	id, _ := res.LastInsertId()
	return s.getAgent(`id = ?`, id)
}

// GetAgentByToken returns the agent owning a plaintext token.
func (s *Store) GetAgentByToken(token string) (model.Agent, error) {
	return s.getAgent(`token_hash = ?`, HashAgentToken(token))
}

// DeleteAgent removes an agent; its token stops working immediately.
func (s *Store) DeleteAgent(id int64) error {
	_, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	return err
}

func (s *Store) getAgent(where string, arg any) (model.Agent, error) {
	var a model.Agent
	var created string
	err := s.db.QueryRow(`SELECT id, name, created_at FROM agents WHERE `+where, arg).Scan(&a.ID, &a.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Agent{}, ErrNotFound
	}
	if err != nil {
		return model.Agent{}, err
	}
	a.CreatedAt = parseTime(created)
	return a, nil
}
