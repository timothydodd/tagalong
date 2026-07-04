// Package registry parses image references and talks to registry v2 APIs.
package registry

import "strings"

// Ref is a parsed container image reference.
type Ref struct {
	Registry string // e.g. docker.io, ghcr.io, reg.dodd.rocks
	Path     string // e.g. timdoddcool/filelink, library/mysql, timothydodd/cadence/api
	Tag      string // e.g. latest, sha-33cefd3 (empty if none / digest given)
	Digest   string // e.g. sha256:... (empty if none)
}

// Repo returns the normalized "registry/path" form used as the app match key.
func (r Ref) Repo() string {
	return r.Registry + "/" + r.Path
}

// ParseRef normalizes a docker-style image reference following Docker's rules:
// a bare name gets docker.io + library/, a single-slash Docker Hub name gets
// docker.io, and the first component is treated as a registry host only if it
// contains a "." or ":" or equals "localhost".
func ParseRef(s string) Ref {
	var ref Ref

	// Split off digest first (image@sha256:...).
	if i := strings.Index(s, "@"); i >= 0 {
		ref.Digest = s[i+1:]
		s = s[:i]
	}

	// Determine registry vs path. Only split on the first slash if the first
	// component looks like a host.
	name := s
	registry := "docker.io"
	if i := strings.Index(s, "/"); i >= 0 {
		first := s[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			registry = first
			name = s[i+1:]
		}
	}

	// Split off tag from the remaining name (but not from a port in a host,
	// which we've already separated).
	if i := strings.LastIndex(name, ":"); i >= 0 {
		ref.Tag = name[i+1:]
		name = name[:i]
	}

	// Docker Hub official images live under library/.
	if registry == "docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}

	ref.Registry = registry
	ref.Path = name
	return ref
}

// NormalizeRepo returns the normalized "registry/path" for an image reference,
// stripping any tag or digest. Used to match webhook payloads to configured apps.
func NormalizeRepo(image string) string {
	return ParseRef(image).Repo()
}
