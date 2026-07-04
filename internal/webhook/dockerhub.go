// Package webhook parses Docker Hub and GitHub container-registry webhooks into
// a normalized image+tag that the deploy engine can act on.
package webhook

import (
	"encoding/json"
	"fmt"

	"github.com/timothydodd/tagalong/internal/registry"
)

// DockerHubPayload is the subset of Docker Hub's webhook body we use.
// See https://docs.docker.com/docker-hub/webhooks/
type DockerHubPayload struct {
	CallbackURL string `json:"callback_url"`
	PushData    struct {
		Tag      string `json:"tag"`
		Pusher   string `json:"pusher"`
		PushedAt int64  `json:"pushed_at"`
	} `json:"push_data"`
	Repository struct {
		RepoName string `json:"repo_name"` // e.g. "timdoddcool/filelink"
		Name     string `json:"name"`
	} `json:"repository"`
}

// ParseDockerHub decodes a Docker Hub webhook body and returns the normalized
// repo ("docker.io/<repo_name>") and the pushed tag.
func ParseDockerHub(body []byte) (repo, tag string, err error) {
	var p DockerHubPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", fmt.Errorf("decode dockerhub payload: %w", err)
	}
	if p.Repository.RepoName == "" {
		return "", "", fmt.Errorf("missing repository.repo_name")
	}
	if p.PushData.Tag == "" {
		return "", "", fmt.Errorf("missing push_data.tag")
	}
	// repo_name is a Docker Hub path (namespace/name); normalize to docker.io.
	repo = registry.NormalizeRepo(p.Repository.RepoName)
	return repo, p.PushData.Tag, nil
}
