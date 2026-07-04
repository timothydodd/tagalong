package store

import (
	"github.com/timothydodd/tagalong/internal/model"
)

// GetSetting returns the value for a settings key, or "" if unset.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		// Missing key is not an error for callers.
		return "", nil
	}
	return v, nil
}

// SetSetting upserts a settings key/value.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ListRegistryCreds returns all stored registry credentials.
func (s *Store) ListRegistryCreds() ([]model.RegistryCred, error) {
	rows, err := s.db.Query(`SELECT registry, username, password FROM registry_creds ORDER BY registry`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	creds := []model.RegistryCred{}
	for rows.Next() {
		var c model.RegistryCred
		if err := rows.Scan(&c.Registry, &c.Username, &c.Password); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

// GetRegistryCred returns credentials for a registry, or ok=false if none exist.
func (s *Store) GetRegistryCred(registry string) (model.RegistryCred, bool, error) {
	var c model.RegistryCred
	err := s.db.QueryRow(`SELECT registry, username, password FROM registry_creds WHERE registry = ?`, registry).
		Scan(&c.Registry, &c.Username, &c.Password)
	if err != nil {
		return model.RegistryCred{}, false, nil
	}
	return c, true, nil
}

// SetRegistryCred upserts credentials for a registry.
func (s *Store) SetRegistryCred(c model.RegistryCred) error {
	_, err := s.db.Exec(`INSERT INTO registry_creds (registry, username, password) VALUES (?, ?, ?)
		ON CONFLICT(registry) DO UPDATE SET username = excluded.username, password = excluded.password`,
		c.Registry, c.Username, c.Password)
	return err
}

// DeleteRegistryCred removes credentials for a registry.
func (s *Store) DeleteRegistryCred(registry string) error {
	_, err := s.db.Exec(`DELETE FROM registry_creds WHERE registry = ?`, registry)
	return err
}
