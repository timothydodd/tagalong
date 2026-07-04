package httpapi

import (
	"bytes"
	"context"
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
	"github.com/timothydodd/tagalong/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func readyDeploy(ns, name, container, image string) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Generation: 1},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: container, Image: image}}},
			},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
	}
}

func testServer(t *testing.T, deps ...*appsv1.Deployment) (http.Handler, *store.Store, *fake.Clientset) {
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
	k8s := deploy.NewK8sWithClient(cs)
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := deploy.NewEngine(k8s, st, bus, nil, log)
	return NewServer(st, engine, k8s, bus, nil, log), st, cs
}

func waitImage(t *testing.T, cs *fake.Clientset, ns, name, container, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
		if err == nil {
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == container && c.Image == want {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("deployment %s/%s image did not reach %q", ns, name, want)
}

func TestDockerHubWebhookDeploys(t *testing.T) {
	dep := readyDeploy("default", "homedash", "robo-dash", "timdoddcool/robo-dash:oldsha")
	srv, st, cs := testServer(t, dep)

	newTag := "4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9"
	app, _ := st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, StrategyConf: model.StrategyConf{Pattern: "^[0-9a-f]{40}$"},
		Enabled: true, WebhookToken: "tok123",
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "homedash", Container: "robo-dash"}},
	})
	_ = app

	body := `{"push_data":{"tag":"` + newTag + `"},"repository":{"repo_name":"timdoddcool/robo-dash"}}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/dockerhub/tok123", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	waitImage(t, cs, "default", "homedash", "robo-dash", "docker.io/timdoddcool/robo-dash:"+newTag)
}

func TestDockerHubWebhookSkipsNonMatchingTag(t *testing.T) {
	dep := readyDeploy("default", "homedash", "robo-dash", "timdoddcool/robo-dash:oldsha")
	srv, st, _ := testServer(t, dep)
	st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, StrategyConf: model.StrategyConf{Pattern: "^[0-9a-f]{40}$"},
		Enabled: true, WebhookToken: "tok123",
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "homedash", Container: "robo-dash"}},
	})

	// ":latest" does not match the 40-hex pattern → skip, recorded as event.
	body := `{"push_data":{"tag":"latest"},"repository":{"repo_name":"timdoddcool/robo-dash"}}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/dockerhub/tok123", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 skip, got %d: %s", rec.Code, rec.Body.String())
	}
	events, _ := st.ListEvents(0, 0, 10)
	if len(events) != 1 || events[0].Status != model.StatusSkipped {
		t.Fatalf("expected 1 skipped event, got %+v", events)
	}
}

func TestDockerHubWebhookUnknownToken(t *testing.T) {
	srv, _, _ := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/hooks/dockerhub/nope", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGitHubWebhookDeploys(t *testing.T) {
	dep := readyDeploy("thorngate", "thorngate", "thorngate", "ghcr.io/timothydodd/thorngate:0.5")
	srv, st, cs := testServer(t, dep)
	st.CreateApp(model.App{
		Name: "thorngate", ImageRepo: "ghcr.io/timothydodd/thorngate",
		TagStrategy: model.StrategySemver, Enabled: true,
		Targets: []model.Target{{Namespace: "thorngate", Kind: model.KindDeployment, Name: "thorngate", Container: "thorngate"}},
	})

	// No webhook secret configured → signature validation is skipped.
	body := `{"action":"published","registry_package":{"name":"thorngate","namespace":"timothydodd","package_type":"container","package_version":{"package_url":"ghcr.io/timothydodd/thorngate:0.6","container_metadata":{"tag":{"name":"0.6"}}}}}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/github", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	waitImage(t, cs, "thorngate", "thorngate", "thorngate", "ghcr.io/timothydodd/thorngate:0.6")
}
