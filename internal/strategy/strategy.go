// Package strategy decides whether an incoming image tag warrants a deploy for
// a given app, and how (patch to a new image, or rollout-restart).
package strategy

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/timothydodd/tagalong/internal/model"
	"github.com/timothydodd/tagalong/internal/registry"
)

// Decision is the outcome of evaluating a candidate tag against an app.
type Decision struct {
	Deploy   bool
	Action   string // model.ActionPatch | model.ActionRestart
	NewImage string // fully-qualified image to patch to (empty for restart)
	Tag      string // tag to record as last-seen
	Reason   string // human-readable explanation (for skips and logs)
}

// skip builds a no-deploy decision with a reason.
func skip(format string, args ...any) Decision {
	return Decision{Deploy: false, Reason: fmt.Sprintf(format, args...)}
}

// TrackTag returns the rolling tag a latest-strategy app follows.
func TrackTag(app model.App) string {
	if app.StrategyConf.TrackTag != "" {
		return app.StrategyConf.TrackTag
	}
	return "latest"
}

// Decide evaluates a pushed/observed tag for an app. currentTag is the tag
// currently deployed in the cluster (read live), used to avoid redundant
// deploys and to compare semver ordering. currentTag may be empty if unknown.
func Decide(app model.App, tag, currentTag string) Decision {
	if !app.Enabled {
		return skip("app disabled")
	}
	switch app.TagStrategy {
	case model.StrategyLatest:
		return decideLatest(app, tag)
	case model.StrategySemver:
		return decideSemver(app, tag, currentTag)
	default: // exact | regex
		return decideExact(app, tag, currentTag)
	}
}

func decideLatest(app model.App, tag string) Decision {
	track := TrackTag(app)
	if tag != "" && tag != track {
		return skip("tag %q is not the tracked tag %q", tag, track)
	}
	// A push of the tracked tag (or a poll-detected digest change) → restart.
	return Decision{
		Deploy: true,
		Action: model.ActionRestart,
		Tag:    track,
		Reason: fmt.Sprintf("rolling tag %q updated", track),
	}
}

func decideExact(app model.App, tag, currentTag string) Decision {
	if tag == "" {
		return skip("no tag in event")
	}
	pattern := app.StrategyConf.Pattern
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return skip("invalid pattern %q: %v", pattern, err)
		}
		if !re.MatchString(tag) {
			return skip("tag %q does not match pattern %q", tag, pattern)
		}
	}
	if tag == currentTag {
		return skip("tag %q already deployed", tag)
	}
	return Decision{
		Deploy:   true,
		Action:   model.ActionPatch,
		NewImage: app.ImageRepo + ":" + tag,
		Tag:      tag,
		Reason:   fmt.Sprintf("deploy tag %q", tag),
	}
}

func decideSemver(app model.App, tag, currentTag string) Decision {
	if tag == "" {
		return skip("no tag in event")
	}
	cand, err := parseSemver(tag)
	if err != nil {
		return skip("tag %q is not semver", tag)
	}
	if c := app.StrategyConf.Constraint; c != "" {
		constraint, err := semver.NewConstraint(c)
		if err != nil {
			return skip("invalid constraint %q: %v", c, err)
		}
		if !constraint.Check(cand) {
			return skip("tag %q fails constraint %q", tag, c)
		}
	}
	// Skip prereleases unless the constraint explicitly allows them.
	if cand.Prerelease() != "" && app.StrategyConf.Constraint == "" {
		return skip("tag %q is a prerelease", tag)
	}
	if currentTag != "" {
		if cur, err := parseSemver(currentTag); err == nil {
			if !cand.GreaterThan(cur) {
				return skip("tag %q not newer than current %q", tag, currentTag)
			}
		}
	}
	return Decision{
		Deploy:   true,
		Action:   model.ActionPatch,
		NewImage: app.ImageRepo + ":" + tag,
		Tag:      tag,
		Reason:   fmt.Sprintf("deploy newer version %q", tag),
	}
}

// parseSemver parses a tag leniently, tolerating a leading "v" and bare
// major.minor forms like "0.6".
func parseSemver(tag string) (*semver.Version, error) {
	return semver.NewVersion(strings.TrimPrefix(tag, "v"))
}

// TagOf returns the tag portion of a deployed image reference.
func TagOf(image string) string {
	return registry.ParseRef(image).Tag
}
