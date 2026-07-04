// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
)

// Config holds all runtime settings.
type Config struct {
	// DBPath is the SQLite file location. In-cluster this is on the PVC at /data.
	DBPath string
	// Listen is the HTTP listen address.
	Listen string
	// Kubeconfig, when set, is used instead of in-cluster config (for local dev).
	Kubeconfig string
}

// Load reads configuration from the environment, applying defaults.
func Load() Config {
	return Config{
		DBPath:     env("TAGALONG_DB_PATH", "/data/tagalong.db"),
		Listen:     env("TAGALONG_LISTEN", ":8080"),
		Kubeconfig: os.Getenv("TAGALONG_KUBECONFIG"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
