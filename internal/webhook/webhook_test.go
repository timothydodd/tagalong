package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

const dockerHubBody = `{
  "callback_url": "https://registry.hub.docker.com/u/timdoddcool/robo-dash/hook/xyz/",
  "push_data": {"pushed_at": 1700000000, "pusher": "timdoddcool", "tag": "4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9"},
  "repository": {"repo_name": "timdoddcool/robo-dash", "name": "robo-dash"}
}`

func TestParseDockerHub(t *testing.T) {
	repo, tag, err := ParseDockerHub([]byte(dockerHubBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "docker.io/timdoddcool/robo-dash" {
		t.Errorf("repo = %q", repo)
	}
	if tag != "4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9" {
		t.Errorf("tag = %q", tag)
	}
}

func TestParseDockerHubMissingFields(t *testing.T) {
	if _, _, err := ParseDockerHub([]byte(`{"repository":{"repo_name":"x/y"}}`)); err == nil {
		t.Error("expected error for missing tag")
	}
	if _, _, err := ParseDockerHub([]byte(`{"push_data":{"tag":"v1"}}`)); err == nil {
		t.Error("expected error for missing repo_name")
	}
}

// registry_package event with a nested path (cadence/api), package_url present.
const githubBody = `{
  "action": "published",
  "registry_package": {
    "name": "cadence/api",
    "namespace": "timothydodd",
    "package_type": "CONTAINER",
    "package_version": {
      "version": "sha256:abc",
      "package_url": "ghcr.io/timothydodd/cadence/api:sha-33cefd3",
      "container_metadata": {"tag": {"name": "sha-33cefd3", "digest": "sha256:abc"}}
    }
  }
}`

func TestParseGitHub(t *testing.T) {
	repo, tag, err := ParseGitHub([]byte(githubBody))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "ghcr.io/timothydodd/cadence/api" {
		t.Errorf("repo = %q", repo)
	}
	if tag != "sha-33cefd3" {
		t.Errorf("tag = %q", tag)
	}
}

// Fallback to namespace/name when package_url is absent.
const githubNoURL = `{
  "action": "published",
  "package": {
    "name": "thorngate",
    "namespace": "timothydodd",
    "package_type": "container",
    "package_version": {"container_metadata": {"tag": {"name": "0.6"}}}
  }
}`

func TestParseGitHubFallbackURL(t *testing.T) {
	repo, tag, err := ParseGitHub([]byte(githubNoURL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "ghcr.io/timothydodd/thorngate" || tag != "0.6" {
		t.Errorf("repo=%q tag=%q", repo, tag)
	}
}

func TestParseGitHubIgnored(t *testing.T) {
	cases := []string{
		`{"action":"deleted","registry_package":{"package_type":"container","package_version":{"container_metadata":{"tag":{"name":"v1"}}}}}`, // wrong action
		`{"action":"published","package":{"package_type":"npm"}}`,                                     // wrong type
		`{"action":"published","registry_package":{"package_type":"container","package_version":{}}}`, // no tag
		`{"zen":"ping"}`, // ping event, no package
	}
	for i, body := range cases {
		if _, _, err := ParseGitHub([]byte(body)); !errors.Is(err, ErrNotContainerPublish) {
			t.Errorf("case %d: expected ErrNotContainerPublish, got %v", i, err)
		}
	}
}

func TestValidateGitHubSignature(t *testing.T) {
	secret := "topsecret"
	body := []byte(`{"hello":"world"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !ValidateGitHubSignature(secret, body, sig) {
		t.Error("valid signature rejected")
	}
	if ValidateGitHubSignature(secret, body, "sha256=deadbeef") {
		t.Error("invalid signature accepted")
	}
	if ValidateGitHubSignature(secret, body, "") {
		t.Error("missing signature accepted")
	}
	// Empty secret disables validation.
	if !ValidateGitHubSignature("", body, "") {
		t.Error("empty secret should accept")
	}
}
