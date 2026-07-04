package poller

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/timothydodd/tagalong/internal/deploy"
	"github.com/timothydodd/tagalong/internal/model"
)

// fakeRegistry implements Registry with canned responses.
type fakeRegistry struct {
	tags    []string
	digest  string
	created map[string]time.Time
}

func (f *fakeRegistry) ListTags(repo string) ([]string, error) { return f.tags, nil }
func (f *fakeRegistry) HeadManifest(repo, tag string) (string, error) {
	if f.digest == "" {
		return "", errors.New("no digest")
	}
	return f.digest, nil
}
func (f *fakeRegistry) ManifestCreated(repo, tag string) (time.Time, error) {
	if t, ok := f.created[tag]; ok {
		return t, nil
	}
	return time.Time{}, errors.New("no created")
}

// fakeEngine captures enqueued jobs.
type fakeEngine struct{ jobs []deploy.Job }

func (e *fakeEngine) Enqueue(j deploy.Job) { e.jobs = append(e.jobs, j) }

// fakeK8s returns a fixed current image.
type fakeK8s struct{ image string }

func (k *fakeK8s) CurrentImage(ctx context.Context, t model.Target) (string, error) {
	return k.image, nil
}

func newPoller(reg Registry, eng Engine, k8s K8s) *Poller {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return New(nil, eng, k8s, reg, log)
}

func TestPollLatestDigestChange(t *testing.T) {
	app := model.App{
		ID: 1, Name: "linqlit", ImageRepo: "reg.dodd.rocks/robododd/linqlit",
		TagStrategy: model.StrategyLatest, Enabled: true, PollEnabled: true,
		LastSeenDigest: "sha256:old",
		Targets:        []model.Target{{Namespace: "linqlit", Name: "api", Container: "api"}},
	}
	reg := &fakeRegistry{digest: "sha256:new"}
	eng := &fakeEngine{}
	p := newPoller(reg, eng, &fakeK8s{image: "reg.dodd.rocks/robododd/linqlit:latest"})

	p.pollApp(context.Background(), app)

	if len(eng.jobs) != 1 || eng.jobs[0].Action != model.ActionRestart || eng.jobs[0].Digest != "sha256:new" {
		t.Fatalf("expected 1 restart job with new digest, got %+v", eng.jobs)
	}
}

func TestPollLatestNoChange(t *testing.T) {
	app := model.App{
		ID: 1, ImageRepo: "x/y", TagStrategy: model.StrategyLatest, Enabled: true,
		LastSeenDigest: "sha256:same",
	}
	reg := &fakeRegistry{digest: "sha256:same"}
	eng := &fakeEngine{}
	p := newPoller(reg, eng, &fakeK8s{})
	p.pollApp(context.Background(), app)
	if len(eng.jobs) != 0 {
		t.Fatalf("expected no jobs when digest unchanged, got %+v", eng.jobs)
	}
}

func TestPollSemverPicksHighest(t *testing.T) {
	app := model.App{
		ID: 1, ImageRepo: "ghcr.io/timothydodd/thorngate", TagStrategy: model.StrategySemver,
		Enabled: true, Targets: []model.Target{{Namespace: "thorngate", Name: "thorngate", Container: "thorngate"}},
	}
	reg := &fakeRegistry{tags: []string{"0.3.0", "0.4", "0.5", "0.7.0", "latest"}}
	eng := &fakeEngine{}
	// Currently running 0.5 → should deploy 0.7.0 (highest valid semver).
	p := newPoller(reg, eng, &fakeK8s{image: "ghcr.io/timothydodd/thorngate:0.5"})
	p.pollApp(context.Background(), app)
	if len(eng.jobs) != 1 || eng.jobs[0].Tag != "0.7.0" {
		t.Fatalf("expected deploy of 0.7.0, got %+v", eng.jobs)
	}
}

func TestPollExactPicksNewestByCreated(t *testing.T) {
	app := model.App{
		ID: 1, ImageRepo: "docker.io/timdoddcool/robo-dash", TagStrategy: model.StrategyExact,
		StrategyConf: model.StrategyConf{Pattern: "^[0-9a-f]{40}$"}, Enabled: true,
		Targets: []model.Target{{Namespace: "default", Name: "homedash", Container: "robo-dash"}},
	}
	old := "1111111111111111111111111111111111111111"
	newer := "2222222222222222222222222222222222222222"
	reg := &fakeRegistry{
		tags: []string{old, newer, "latest"},
		created: map[string]time.Time{
			old:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			newer: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	eng := &fakeEngine{}
	p := newPoller(reg, eng, &fakeK8s{image: "docker.io/timdoddcool/robo-dash:" + old})
	p.pollApp(context.Background(), app)
	if len(eng.jobs) != 1 || eng.jobs[0].Tag != newer {
		t.Fatalf("expected deploy of newest tag %s, got %+v", newer, eng.jobs)
	}
}
