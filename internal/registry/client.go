package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// manifestAccept lists the manifest media types we accept, covering Docker v2,
// OCI, and multi-arch index/list forms.
var manifestAccept = strings.Join([]string{
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.index.v1+json",
}, ", ")

// Client talks to Docker Registry v2 HTTP APIs with bearer-token auth.
type Client struct {
	http   *http.Client
	creds  CredResolver
	tokens *tokenCache
}

// NewClient builds a registry client. creds may be nil for anonymous-only.
func NewClient(creds CredResolver) *Client {
	return &Client{
		http:   &http.Client{Timeout: 20 * time.Second},
		creds:  creds,
		tokens: newTokenCache(),
	}
}

// apiHost maps a normalized registry name to its v2 API host. docker.io's API
// lives at registry-1.docker.io; other registries serve v2 at their own host.
func apiHost(registry string) string {
	if registry == "docker.io" {
		return "registry-1.docker.io"
	}
	return registry
}

// ListTags returns the tags available for a normalized "registry/path" repo.
func (c *Client) ListTags(repo string) ([]string, error) {
	ref := ParseRef(repo)
	url := fmt.Sprintf("https://%s/v2/%s/tags/list", apiHost(ref.Registry), ref.Path)

	var out struct {
		Tags []string `json:"tags"`
	}
	// Docker Hub paginates via Link headers; follow them.
	next := url + "?n=100"
	for next != "" {
		resp, err := c.do(http.MethodGet, next, ref.Registry, ref.Path, "")
		if err != nil {
			return nil, err
		}
		var page struct {
			Tags []string `json:"tags"`
		}
		body, _ := io.ReadAll(resp.Body)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list tags %s: %s: %s", repo, resp.Status, truncate(body))
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		out.Tags = append(out.Tags, page.Tags...)
		next = nextLink(link, ref.Registry)
	}
	sort.Strings(out.Tags)
	if out.Tags == nil {
		out.Tags = []string{} // return [] not null for a repo with no tags
	}
	return out.Tags, nil
}

// HeadManifest returns the content digest for a repo:tag without downloading the
// manifest body. Used to detect changes to rolling tags like :latest.
func (c *Client) HeadManifest(repo, tag string) (string, error) {
	ref := ParseRef(repo)
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", apiHost(ref.Registry), ref.Path, tag)
	resp, err := c.do(http.MethodHead, url, ref.Registry, ref.Path, manifestAccept)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("head manifest %s:%s: %s", repo, tag, resp.Status)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no Docker-Content-Digest for %s:%s", repo, tag)
	}
	return digest, nil
}

// ManifestCreated returns the image's build timestamp for a repo:tag by reading
// the config blob's `.created` field. Handles multi-arch indexes by following
// the first sub-manifest. Best-effort: callers should tolerate errors.
func (c *Client) ManifestCreated(repo, tag string) (time.Time, error) {
	ref := ParseRef(repo)
	man, err := c.getManifest(ref, tag)
	if err != nil {
		return time.Time{}, err
	}
	// If this is an index/list, descend into the first sub-manifest.
	if len(man.Manifests) > 0 {
		man, err = c.getManifestByDigest(ref, man.Manifests[0].Digest)
		if err != nil {
			return time.Time{}, err
		}
	}
	if man.Config.Digest == "" {
		return time.Time{}, fmt.Errorf("manifest for %s:%s has no config", repo, tag)
	}
	created, err := c.getConfigCreated(ref, man.Config.Digest)
	if err != nil {
		return time.Time{}, err
	}
	return created, nil
}

type manifestDoc struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

func (c *Client) getManifest(ref Ref, tag string) (manifestDoc, error) {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", apiHost(ref.Registry), ref.Path, tag)
	return c.getManifestURL(ref, url)
}

func (c *Client) getManifestByDigest(ref Ref, digest string) (manifestDoc, error) {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", apiHost(ref.Registry), ref.Path, digest)
	return c.getManifestURL(ref, url)
}

func (c *Client) getManifestURL(ref Ref, url string) (manifestDoc, error) {
	var doc manifestDoc
	resp, err := c.do(http.MethodGet, url, ref.Registry, ref.Path, manifestAccept)
	if err != nil {
		return doc, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return doc, fmt.Errorf("get manifest: %s: %s", resp.Status, truncate(body))
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return doc, err
	}
	return doc, nil
}

func (c *Client) getConfigCreated(ref Ref, digest string) (time.Time, error) {
	url := fmt.Sprintf("https://%s/v2/%s/blobs/%s", apiHost(ref.Registry), ref.Path, digest)
	resp, err := c.do(http.MethodGet, url, ref.Registry, ref.Path, "")
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("get config blob: %s", resp.Status)
	}
	var cfg struct {
		Created time.Time `json:"created"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return time.Time{}, err
	}
	return cfg.Created, nil
}

// do issues a request, transparently performing the bearer-token dance on 401.
func (c *Client) do(method, url, registry, path, accept string) (*http.Response, error) {
	req, err := c.newReq(method, url, registry, path, accept)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	// Parse the challenge, fetch a token, retry once.
	wh := resp.Header.Get("WWW-Authenticate")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	ch, ok := parseBearerChallenge(wh)
	if !ok {
		return nil, fmt.Errorf("401 without bearer challenge from %s", registry)
	}
	key := registry + "|" + ch.scope
	token, cached := c.tokens.get(key)
	if !cached {
		var ttl time.Duration
		token, ttl, err = c.fetchToken(ch, registry)
		if err != nil {
			return nil, err
		}
		c.tokens.put(key, token, ttl)
	}

	req2, err := c.newReq(method, url, registry, path, accept)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Authorization", "Bearer "+token)
	return c.http.Do(req2)
}

func (c *Client) newReq(method, url, registry, path, accept string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	// Reuse a cached token if we already have one for the pull scope.
	scope := "repository:" + path + ":pull"
	if token, ok := c.tokens.get(registry + "|" + scope); ok {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

// nextLink extracts the URL from a registry pagination Link header, resolving it
// against the API host. Returns "" when there is no next page.
func nextLink(link, registry string) string {
	if link == "" || !strings.Contains(link, `rel="next"`) {
		return ""
	}
	start := strings.Index(link, "<")
	end := strings.Index(link, ">")
	if start < 0 || end < 0 || end <= start {
		return ""
	}
	path := link[start+1 : end]
	if strings.HasPrefix(path, "http") {
		return path
	}
	return "https://" + apiHost(registry) + path
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}
