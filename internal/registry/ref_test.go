package registry

import "testing"

func TestParseRef(t *testing.T) {
	tests := []struct {
		in       string
		registry string
		path     string
		tag      string
		digest   string
	}{
		{"timdoddcool/filelink:abc123", "docker.io", "timdoddcool/filelink", "abc123", ""},
		{"timdoddcool/filelink", "docker.io", "timdoddcool/filelink", "", ""},
		{"mysql:8.4", "docker.io", "library/mysql", "8.4", ""},
		{"mysql", "docker.io", "library/mysql", "", ""},
		{"ghcr.io/timothydodd/thorngate:0.6", "ghcr.io", "timothydodd/thorngate", "0.6", ""},
		{"ghcr.io/timothydodd/cadence/api:sha-33cefd3", "ghcr.io", "timothydodd/cadence/api", "sha-33cefd3", ""},
		{"registry.example.com/robododd/linqlit:728c9810", "registry.example.com", "robododd/linqlit", "728c9810", ""},
		{"docker.io/timdoddcool/terraria-proxy:latest", "docker.io", "timdoddcool/terraria-proxy", "latest", ""},
		{"localhost:5000/app:v1", "localhost:5000", "app", "v1", ""},
		{"repo@sha256:deadbeef", "docker.io", "library/repo", "", "sha256:deadbeef"},
		{"ghcr.io/x/y@sha256:abc", "ghcr.io", "x/y", "", "sha256:abc"},
	}
	for _, tt := range tests {
		got := ParseRef(tt.in)
		if got.Registry != tt.registry || got.Path != tt.path || got.Tag != tt.tag || got.Digest != tt.digest {
			t.Errorf("ParseRef(%q) = %+v, want registry=%s path=%s tag=%s digest=%s",
				tt.in, got, tt.registry, tt.path, tt.tag, tt.digest)
		}
	}
}

func TestNormalizeRepo(t *testing.T) {
	tests := map[string]string{
		"timdoddcool/filelink:abc":           "docker.io/timdoddcool/filelink",
		"mysql:8.4":                          "docker.io/library/mysql",
		"ghcr.io/timothydodd/cadence/api:x":  "ghcr.io/timothydodd/cadence/api",
		"registry.example.com/robododd/linqlit:x":  "registry.example.com/robododd/linqlit",
	}
	for in, want := range tests {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
