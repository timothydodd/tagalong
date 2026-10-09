package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/events"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/relay"
	"github.com/timothydodd/tagalong/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// instance is one tagalong (hub or agent) with its own store and fake cluster.
type instance struct {
	st      *store.Store
	cs      *fake.Clientset
	k8s     *deploy.K8s
	bus     *events.Bus
	engine  *deploy.Engine
	log     *slog.Logger
	hub     *relay.Hub
	handler http.Handler
}

func newInstance(t *testing.T, deps ...*appsv1.Deployment) *instance {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := fake.NewSimpleClientset()
	for _, d := range deps {
		cs.AppsV1().Deployments(d.Namespace).Create(context.Background(), d, metav1.CreateOptions{})
	}
	in := &instance{st: st, cs: cs, k8s: deploy.NewK8sWithClient(cs), bus: events.NewBus(),
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	in.engine = deploy.NewEngine(in.k8s, st, in.bus, nil, in.log)
	in.hub = relay.NewHub(st, in.log)
	if err := SeedAdmin(st, in.log); err != nil {
		t.Fatal(err)
	}
	in.handler = NewServer(st, in.engine, in.k8s, in.bus, nil, in.log, WithHub(in.hub))
	return in
}

// hubAndAgent starts a hub over real HTTP with one registered agent ("office")
// whose cluster runs deps, and connects the agent to it.
func hubAndAgent(t *testing.T, deps ...*appsv1.Deployment) (hub *instance, hubURL string, agent *instance) {
	t.Helper()
	hub = newInstance(t)
	ts := httptest.NewServer(hub.handler)
	t.Cleanup(ts.Close)
	if _, err := hub.st.CreateAgent("office", "agent-secret-token"); err != nil {
		t.Fatal(err)
	}

	agent = newInstance(t, deps...)
	client := relay.NewClient(ts.URL, "agent-secret-token",
		NewRelayHandler(agent.st, agent.engine, agent.k8s, agent.bus, nil, agent.log), agent.log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go client.Run(ctx)

	waitConnected(t, hub, true)
	if !client.Status().Connected {
		t.Fatalf("client status not connected: %+v", client.Status())
	}
	return hub, ts.URL, agent
}

func waitConnected(t *testing.T, in *instance, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		agents, _ := in.st.ListAgents()
		agents = in.hub.Statuses(agents)
		if len(agents) > 0 && agents[0].Connected == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("agent connected never became %v", want)
}

type relayBody struct {
	Status string              `json:"status"`
	Agents []relay.AgentResult `json:"agents"`
}

func postHook(t *testing.T, url string, body string, hdr map[string]string) (int, relayBody, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	var rb relayBody
	json.Unmarshal(buf.Bytes(), &rb)
	return resp.StatusCode, rb, buf.String()
}

func TestRelayDockerHubToAgent(t *testing.T) {
	dep := readyDeploy("default", "homedash", "robo-dash", "timdoddcool/robo-dash:oldsha")
	_, hubURL, agent := hubAndAgent(t, dep)
	agent.st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, StrategyConf: model.StrategyConf{Pattern: "^[0-9a-f]{40}$"},
		Enabled: true, WebhookToken: "agenttok",
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "homedash", Container: "robo-dash"}},
	})

	newTag := "4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9"
	body := `{"push_data":{"tag":"` + newTag + `"},"repository":{"repo_name":"timdoddcool/robo-dash"}}`
	code, rb, raw := postHook(t, hubURL+"/hooks/dockerhub/agenttok", body, nil)
	if code != http.StatusAccepted || rb.Status != "relayed" {
		t.Fatalf("expected 202 relayed, got %d: %s", code, raw)
	}
	if len(rb.Agents) != 1 || rb.Agents[0].Agent != "office" || rb.Agents[0].State != relay.StateDone || rb.Agents[0].Status != http.StatusAccepted {
		t.Fatalf("unexpected agent results: %s", raw)
	}
	waitImage(t, agent.cs, "default", "homedash", "robo-dash", "docker.io/timdoddcool/robo-dash:"+newTag)
}

func TestRelayDockerHubUnknownEverywhere(t *testing.T) {
	_, hubURL, _ := hubAndAgent(t)
	code, _, raw := postHook(t, hubURL+"/hooks/dockerhub/nope", `{}`, nil)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404 when no agent owns the token, got %d: %s", code, raw)
	}
}

func TestRelayGitHubTrustsHubSignature(t *testing.T) {
	dep := readyDeploy("thorngate", "thorngate", "thorngate", "ghcr.io/timothydodd/thorngate:0.5")
	hub, hubURL, agent := hubAndAgent(t, dep)
	agent.st.CreateApp(model.App{
		Name: "thorngate", ImageRepo: "ghcr.io/timothydodd/thorngate",
		TagStrategy: model.StrategySemver, Enabled: true,
		Targets: []model.Target{{Namespace: "thorngate", Kind: model.KindDeployment, Name: "thorngate", Container: "thorngate"}},
	})
	// The hub verifies against its secret; the agent's (different) secret is
	// irrelevant for relayed payloads.
	hub.st.SetSetting(model.KeyGitHubWebhookSecret, "hub-secret")
	agent.st.SetSetting(model.KeyGitHubWebhookSecret, "agent-secret")

	body := `{"action":"published","registry_package":{"name":"thorngate","namespace":"timothydodd","package_type":"container","package_version":{"package_url":"ghcr.io/timothydodd/thorngate:0.6","container_metadata":{"tag":{"name":"0.6"}}}}}`

	// Bad signature is rejected at the hub, never relayed.
	if code, _, raw := postHook(t, hubURL+"/hooks/github", body, map[string]string{"X-Hub-Signature-256": "sha256=00"}); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad signature, got %d: %s", code, raw)
	}

	mac := hmac.New(sha256.New, []byte("hub-secret"))
	mac.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	code, rb, raw := postHook(t, hubURL+"/hooks/github", body, map[string]string{"X-Hub-Signature-256": sig})
	if code != http.StatusAccepted || len(rb.Agents) != 1 || rb.Agents[0].Status != http.StatusAccepted {
		t.Fatalf("expected relayed 202, got %d: %s", code, raw)
	}
	waitImage(t, agent.cs, "thorngate", "thorngate", "thorngate", "ghcr.io/timothydodd/thorngate:0.6")
}

func TestRelayHubAppTakesPrecedence(t *testing.T) {
	hubDep := readyDeploy("default", "homedash", "robo-dash", "timdoddcool/robo-dash:oldsha")
	hub, hubURL, _ := hubAndAgent(t)
	hub.cs.AppsV1().Deployments("default").Create(context.Background(), hubDep, metav1.CreateOptions{})
	hub.st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, StrategyConf: model.StrategyConf{Pattern: "^[0-9a-f]{40}$"},
		Enabled: true, WebhookToken: "hubtok",
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "homedash", Container: "robo-dash"}},
	})
	newTag := "4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9"
	body := `{"push_data":{"tag":"` + newTag + `"},"repository":{"repo_name":"timdoddcool/robo-dash"}}`
	code, rb, raw := postHook(t, hubURL+"/hooks/dockerhub/hubtok", body, nil)
	if code != http.StatusAccepted || rb.Status == "relayed" {
		t.Fatalf("expected local deploy, got %d: %s", code, raw)
	}
	waitImage(t, hub.cs, "default", "homedash", "robo-dash", "docker.io/timdoddcool/robo-dash:"+newTag)
}

func TestRelayOfflineAgentQueues(t *testing.T) {
	hub := newInstance(t)
	ts := httptest.NewServer(hub.handler)
	t.Cleanup(ts.Close)
	hub.st.CreateAgent("office", "agent-secret-token")

	// Nobody is polling, so the hook is queued and reported as such —
	// immediately, without holding the request open for relayWait.
	start := time.Now()
	code, rb, raw := postHook(t, ts.URL+"/hooks/dockerhub/sometoken", `{}`, nil)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("offline-only relay should not wait, took %v", took)
	}
	if code != http.StatusAccepted || len(rb.Agents) != 1 || rb.Agents[0].State != relay.StateQueued {
		t.Fatalf("expected 202 queued, got %d: %s", code, raw)
	}

	// The queued message is delivered on the agent's next poll.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+relay.PathPoll, nil)
	req.Header.Set("Authorization", "Bearer agent-secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var msgs []relay.Message
	json.NewDecoder(resp.Body).Decode(&msgs)
	if len(msgs) != 1 || msgs[0].Kind != relay.KindDockerHub || msgs[0].Token != "sometoken" {
		t.Fatalf("expected the queued message, got %+v", msgs)
	}
}

func TestRelayPollRejectsBadToken(t *testing.T) {
	hub := newInstance(t)
	hub.st.CreateAgent("office", "agent-secret-token")
	for _, hdr := range []string{"", "Bearer wrong"} {
		req := httptest.NewRequest(http.MethodGet, relay.PathPoll, nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		hub.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth %q: expected 401, got %d", hdr, rec.Code)
		}
	}
}

func TestAgentsAPI(t *testing.T) {
	srv, st, _ := testServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"office"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	if len(created.Token) != 64 {
		t.Fatalf("expected a 64-hex token, got %q", created.Token)
	}
	if a, err := st.GetAgentByToken(created.Token); err != nil || a.Name != "office" {
		t.Fatalf("token does not resolve to agent: %+v %v", a, err)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"office"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: expected 409, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"bad name!"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name: expected 400, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"office"`)) || bytes.Contains(rec.Body.Bytes(), []byte(created.Token)) {
		t.Fatalf("list should show the agent but never its token: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/agents/"+jsonNum(created.ID), nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := st.GetAgentByToken(created.Token); err == nil {
		t.Fatal("token still valid after delete")
	}
}

func jsonNum(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
