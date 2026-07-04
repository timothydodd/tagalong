package deploy

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/timothydodd/tagalong/internal/events"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// readyDeployment builds a Deployment already reporting healthy so the
// fake-client rollout watch returns immediately.
func readyDeployment(ns, name, container, image string) *appsv1.Deployment {
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
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, ReadyReplicas: 1,
			UpdatedReplicas: 1, AvailableReplicas: 1,
		},
	}
}

func newTestEngine(t *testing.T, objs ...*appsv1.Deployment) (*Engine, *store.Store, *fake.Clientset) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cs := fake.NewSimpleClientset()
	for _, o := range objs {
		if _, err := cs.AppsV1().Deployments(o.Namespace).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed deployment: %v", err)
		}
	}
	k8s := NewK8sWithClient(cs)
	bus := events.NewBus()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewEngine(k8s, st, bus, nil, log), st, cs
}

func TestDeploySyncPatch(t *testing.T) {
	dep := readyDeployment("default", "robo-dash", "robo-dash", "timdoddcool/robo-dash:oldsha")
	engine, st, cs := newTestEngine(t, dep)

	app, err := st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, Enabled: true,
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "robo-dash", Container: "robo-dash"}},
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	ev := engine.DeploySync(context.Background(), Job{
		App: app, Trigger: model.TriggerManual, Action: model.ActionPatch,
		NewImage: "docker.io/timdoddcool/robo-dash:newsha", Tag: "newsha",
	})
	if ev.Status != model.StatusSuccess {
		t.Fatalf("expected success, got %s: %s", ev.Status, ev.Detail)
	}

	// Live deployment image updated.
	got, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "robo-dash", metav1.GetOptions{})
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "docker.io/timdoddcool/robo-dash:newsha" {
		t.Errorf("image not patched: got %s", img)
	}

	// History recorded with old→new.
	events, _ := st.ListEvents(app.ID, 0, 10)
	if len(events) != 1 || events[0].Status != model.StatusSuccess {
		t.Fatalf("expected 1 success event, got %+v", events)
	}
	if events[0].OldImage != "timdoddcool/robo-dash:oldsha" || events[0].NewImage != "docker.io/timdoddcool/robo-dash:newsha" {
		t.Errorf("unexpected history images: old=%s new=%s", events[0].OldImage, events[0].NewImage)
	}

	// last_seen recorded.
	reloaded, _ := st.GetApp(app.ID)
	if reloaded.LastSeenTag != "newsha" {
		t.Errorf("last_seen_tag = %q, want newsha", reloaded.LastSeenTag)
	}
}

func TestDeploySyncRestart(t *testing.T) {
	dep := readyDeployment("default", "jellyfin", "jellyfin", "jellyfin/jellyfin:latest")
	engine, st, cs := newTestEngine(t, dep)

	app, _ := st.CreateApp(model.App{
		Name: "jellyfin", ImageRepo: "docker.io/jellyfin/jellyfin",
		TagStrategy: model.StrategyLatest, Enabled: true,
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "jellyfin", Container: "jellyfin"}},
	})

	ev := engine.DeploySync(context.Background(), Job{App: app, Trigger: model.TriggerManual, Action: model.ActionRestart})
	if ev.Status != model.StatusSuccess {
		t.Fatalf("expected success, got %s: %s", ev.Status, ev.Detail)
	}

	// Restart annotation set, image unchanged.
	got, _ := cs.AppsV1().Deployments("default").Get(context.Background(), "jellyfin", metav1.GetOptions{})
	if _, ok := got.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; !ok {
		t.Error("restartedAt annotation not set")
	}
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "jellyfin/jellyfin:latest" {
		t.Errorf("image should be unchanged, got %s", img)
	}
}

// fakePurger records whether Purge was called.
type fakePurger struct {
	called bool
	err    error
}

func (f *fakePurger) Purge(ctx context.Context, app model.App) error {
	f.called = true
	return f.err
}

func TestDeploySyncRunsCloudflarePurge(t *testing.T) {
	dep := readyDeployment("default", "robo-dash", "robo-dash", "timdoddcool/robo-dash:old")
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := fake.NewSimpleClientset()
	cs.AppsV1().Deployments("default").Create(context.Background(), dep, metav1.CreateOptions{})
	purger := &fakePurger{}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := NewEngine(NewK8sWithClient(cs), st, events.NewBus(), purger, log)

	app, _ := st.CreateApp(model.App{
		Name: "robo-dash", ImageRepo: "docker.io/timdoddcool/robo-dash",
		TagStrategy: model.StrategyExact, Enabled: true,
		CFPurge: model.CFPurge{Enabled: true, ZoneID: "z", Mode: model.CFModeEverything},
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "robo-dash", Container: "robo-dash"}},
	})

	ev := engine.DeploySync(context.Background(), Job{App: app, Trigger: model.TriggerManual, Action: model.ActionPatch, NewImage: "docker.io/timdoddcool/robo-dash:new", Tag: "new"})
	if ev.Status != model.StatusSuccess {
		t.Fatalf("expected success, got %s: %s", ev.Status, ev.Detail)
	}
	if !purger.called {
		t.Error("expected purger to be called")
	}
	if !ev.CFPurged {
		t.Error("expected cf_purged to be true on event")
	}
}

func TestDeploySyncMissingWorkload(t *testing.T) {
	engine, st, _ := newTestEngine(t) // no deployments seeded

	app, _ := st.CreateApp(model.App{
		Name: "ghost", ImageRepo: "docker.io/x/ghost",
		TagStrategy: model.StrategyExact, Enabled: true,
		Targets: []model.Target{{Namespace: "default", Kind: model.KindDeployment, Name: "ghost", Container: "ghost"}},
	})

	ev := engine.DeploySync(context.Background(), Job{
		App: app, Trigger: model.TriggerManual, Action: model.ActionPatch, NewImage: "docker.io/x/ghost:v2",
	})
	if ev.Status != model.StatusFailed {
		t.Fatalf("expected failure for missing workload, got %s", ev.Status)
	}
}
