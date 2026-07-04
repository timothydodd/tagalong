package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/timothydodd/tagalong/internal/registry"
)

// gitHubPackagePayload is the subset of GitHub's package / registry_package
// webhook we use. GitHub sends the payload under "registry_package" for the
// registry_package event and "package" for the package event; both share this
// shape, so we decode both keys.
type gitHubPackagePayload struct {
	Action          string      `json:"action"`
	RegistryPackage *githubPkg  `json:"registry_package"`
	Package         *githubPkg  `json:"package"`
}

type githubPkg struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	PackageType    string `json:"package_type"`
	PackageVersion struct {
		Version          string `json:"version"`
		PackageURL       string `json:"package_url"`
		ContainerMetadata struct {
			Tag struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			} `json:"tag"`
		} `json:"container_metadata"`
	} `json:"package_version"`
}

// ValidateGitHubSignature checks the X-Hub-Signature-256 header against the body
// using the shared secret. Returns true when valid. An empty secret disables
// validation (returns true) so unconfigured setups still work on a trusted LAN.
func ValidateGitHubSignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return true
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	got := strings.TrimPrefix(header, prefix)
	return hmac.Equal([]byte(expected), []byte(got))
}

// ErrNotContainerPublish signals a GitHub event we intentionally ignore (wrong
// action, non-container package, or no tag). The hook handler treats it as a
// benign no-op (HTTP 200).
var ErrNotContainerPublish = fmt.Errorf("not a container publish event")

// ParseGitHub decodes a GitHub package/registry_package webhook and returns the
// normalized image repo (ghcr.io/...) and tag. Returns ErrNotContainerPublish
// for events that should be ignored rather than errored.
func ParseGitHub(body []byte) (repo, tag string, err error) {
	var p gitHubPackagePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", fmt.Errorf("decode github payload: %w", err)
	}
	pkg := p.RegistryPackage
	if pkg == nil {
		pkg = p.Package
	}
	if pkg == nil {
		return "", "", ErrNotContainerPublish
	}
	if p.Action != "published" && p.Action != "updated" {
		return "", "", ErrNotContainerPublish
	}
	if !strings.EqualFold(pkg.PackageType, "container") {
		return "", "", ErrNotContainerPublish
	}
	tag = pkg.PackageVersion.ContainerMetadata.Tag.Name
	if tag == "" {
		// Digest-only push (no tag) — nothing to deploy on.
		return "", "", ErrNotContainerPublish
	}

	// Prefer package_url (carries the full ghcr.io/owner/path:tag, correct even
	// for nested paths like cadence/api); fall back to namespace/name.
	if u := pkg.PackageVersion.PackageURL; u != "" {
		repo = registry.NormalizeRepo(u)
	} else if pkg.Namespace != "" {
		repo = registry.NormalizeRepo("ghcr.io/" + pkg.Namespace + "/" + pkg.Name)
	} else {
		repo = registry.NormalizeRepo("ghcr.io/" + pkg.Name)
	}
	return repo, tag, nil
}
