package strategy

import (
	"testing"

	"github.com/timothydodd/tagalong/internal/model"
)

func app(strat string, conf model.StrategyConf) model.App {
	return model.App{
		Name:         "test",
		ImageRepo:    "docker.io/timdoddcool/robo-dash",
		TagStrategy:  strat,
		StrategyConf: conf,
		Enabled:      true,
	}
}

func TestDecideExact(t *testing.T) {
	shaPattern := "^[0-9a-f]{40}$"
	a := app(model.StrategyExact, model.StrategyConf{Pattern: shaPattern})

	// Matching 40-hex tag, different from current → deploy.
	d := Decide(a, "4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9", "oldsha")
	if !d.Deploy || d.Action != model.ActionPatch {
		t.Fatalf("expected patch deploy, got %+v", d)
	}
	if d.NewImage != "docker.io/timdoddcool/robo-dash:4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9" {
		t.Errorf("unexpected new image: %s", d.NewImage)
	}

	// Same tag already deployed → skip.
	if d := Decide(a, "abc", "abc"); d.Deploy {
		t.Errorf("expected skip for same tag, got %+v", d)
	}

	// Non-matching tag (e.g. :latest) → skip.
	if d := Decide(a, "latest", "oldsha"); d.Deploy {
		t.Errorf("expected skip for non-matching tag, got %+v", d)
	}
}

func TestDecideLatest(t *testing.T) {
	a := app(model.StrategyLatest, model.StrategyConf{})

	// Push of default tracked tag → restart.
	d := Decide(a, "latest", "")
	if !d.Deploy || d.Action != model.ActionRestart {
		t.Fatalf("expected restart, got %+v", d)
	}

	// Push of a different tag → skip.
	if d := Decide(a, "v1.2.3", ""); d.Deploy {
		t.Errorf("expected skip for non-tracked tag, got %+v", d)
	}

	// Custom track tag.
	b := app(model.StrategyLatest, model.StrategyConf{TrackTag: "main"})
	if d := Decide(b, "main", ""); !d.Deploy || d.Action != model.ActionRestart {
		t.Errorf("expected restart for tracked tag 'main', got %+v", d)
	}
}

func TestDecideSemver(t *testing.T) {
	a := app(model.StrategySemver, model.StrategyConf{})

	// Newer version → deploy.
	if d := Decide(a, "0.7", "0.6"); !d.Deploy || d.NewImage != "docker.io/timdoddcool/robo-dash:0.7" {
		t.Errorf("expected deploy of 0.7, got %+v", d)
	}
	// Older/equal → skip.
	if d := Decide(a, "0.5", "0.6"); d.Deploy {
		t.Errorf("expected skip for older version, got %+v", d)
	}
	if d := Decide(a, "0.6", "0.6"); d.Deploy {
		t.Errorf("expected skip for equal version, got %+v", d)
	}
	// Leading v tolerated.
	if d := Decide(a, "v1.0.0", "0.6"); !d.Deploy {
		t.Errorf("expected deploy of v1.0.0, got %+v", d)
	}
	// Prerelease skipped without a constraint.
	if d := Decide(a, "1.0.0-rc1", "0.6"); d.Deploy {
		t.Errorf("expected skip for prerelease, got %+v", d)
	}
	// Non-semver skipped.
	if d := Decide(a, "sha-33cefd3", "0.6"); d.Deploy {
		t.Errorf("expected skip for non-semver, got %+v", d)
	}
}

func TestDecideDisabled(t *testing.T) {
	a := app(model.StrategyExact, model.StrategyConf{})
	a.Enabled = false
	if d := Decide(a, "abc", ""); d.Deploy {
		t.Errorf("expected skip for disabled app, got %+v", d)
	}
}
