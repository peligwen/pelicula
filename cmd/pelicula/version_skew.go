package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Image-staleness detection.
//
// The pelicula-api and procula images are only built explicitly (pelicula
// rebuild / redeploy, or implicitly on first `up` when missing) — a plain
// `git pull` + `pelicula up` starts the stack with images built from older
// sources while nginx's bind-mounted dashboard files are already current.
// That half-deployed state is invisible in container status: everything
// reports healthy while the new frontend calls endpoints the old API does
// not have. Both `up` and `doctor` compare each image's
// org.pelicula.version label (stamped at build time from git describe)
// against the repo and flag images whose source directory has moved on.

// skewServices maps the two locally-built compose services to the source
// directory (repo-relative, also the image build context) whose commits
// invalidate their images.
var skewServices = []struct{ service, srcDir string }{
	{"pelicula-api", "middleware"},
	{"procula", "procula"},
}

type skewStatus int

const (
	skewCurrent   skewStatus = iota
	skewStale                // commits touching srcDir landed after the image build
	skewDirty                // image matches HEAD but srcDir has uncommitted changes
	skewUnstamped            // image predates version labels, or was built without git
	skewMissing              // image not present on this host
	skewUnknown              // label present but names a commit unknown to this repo
)

// skewReport is the assessment of one locally-built image against the repo.
type skewReport struct {
	Service      string
	SrcDir       string
	ImageVersion string // raw label value ("" when missing or unlabeled)
	Status       skewStatus
	Behind       int // commits touching SrcDir since the image build (skewStale only)
}

// needsRedeploy reports whether this is a state `pelicula redeploy` fixes.
// Missing images are excluded (compose builds them on up), as is skewUnknown
// (rebuilding from a repo that doesn't know the image's commit proves
// nothing about staleness — the operator has to judge that one).
func (r skewReport) needsRedeploy() bool {
	return r.Status == skewStale || r.Status == skewDirty || r.Status == skewUnstamped
}

// verdict renders a one-line human-readable assessment for up/doctor output.
func (r skewReport) verdict() string {
	switch r.Status {
	case skewCurrent:
		return fmt.Sprintf("%s image %s — up to date", r.Service, r.ImageVersion)
	case skewStale:
		noun := "commits"
		if r.Behind == 1 {
			noun = "commit"
		}
		return fmt.Sprintf("%s image %s — STALE: %d %s behind %s/ (run: pelicula redeploy)",
			r.Service, r.ImageVersion, r.Behind, noun, r.SrcDir)
	case skewDirty:
		return fmt.Sprintf("%s image %s — matches HEAD, but %s/ has uncommitted changes (run: pelicula redeploy)",
			r.Service, r.ImageVersion, r.SrcDir)
	case skewUnstamped:
		return fmt.Sprintf("%s image — no version stamp; staleness unknown (run: pelicula redeploy)", r.Service)
	case skewMissing:
		return fmt.Sprintf("%s image — not built yet", r.Service)
	default:
		return fmt.Sprintf("%s image %s — built from a commit unknown to this repo; staleness unknown",
			r.Service, r.ImageVersion)
	}
}

var describeHashPattern = regexp.MustCompile(`-g([0-9a-f]{7,40})$`)

// extractCommit reduces a git-describe string to the rev it names:
// "887fd50" stays as-is, "v1.2-47-g1304ae3" yields "1304ae3", and a
// "-dirty" suffix is stripped first. Tag-exact describes ("v1.2") pass
// through unchanged — rev-list resolves tag names just as well.
func extractCommit(version string) string {
	v := strings.TrimSuffix(version, "-dirty")
	if m := describeHashPattern.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return v
}

// classifySkew derives a skewReport from raw inputs: the image label value
// (labelErr non-nil means the image is missing), the number of commits
// touching srcDir since the labeled commit (behindErr non-nil means the
// commit is unknown here), and whether srcDir has uncommitted changes.
// Kept pure — all git/docker plumbing stays in checkImageSkews — so the
// decision table is unit-testable.
func classifySkew(service, srcDir, label string, labelErr error, behind int, behindErr error, dirty bool) skewReport {
	r := skewReport{Service: service, SrcDir: srcDir, ImageVersion: label}
	switch {
	case labelErr != nil:
		r.Status = skewMissing
		r.ImageVersion = ""
	case label == "" || label == "dev":
		r.Status = skewUnstamped
	case behindErr != nil:
		r.Status = skewUnknown
	case behind > 0:
		r.Status = skewStale
		r.Behind = behind
	case dirty:
		r.Status = skewDirty
	default:
		r.Status = skewCurrent
	}
	return r
}

// imageVersionLabel reads the org.pelicula.version label from a local image.
// Returns "" (no error) when the image exists but carries no label.
func imageVersionLabel(c *Compose, image string) (string, error) {
	out, err := c.DockerRaw("image", "inspect",
		"--format", `{{index .Config.Labels "org.pelicula.version"}}`, image)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// commitsBehind counts commits in rev..HEAD that touch srcDir.
func commitsBehind(repoDir, rev, srcDir string) (int, error) {
	out, err := exec.Command("git", "-C", repoDir,
		"rev-list", "--count", rev+"..HEAD", "--", srcDir).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// srcDirIsDirty reports whether srcDir has uncommitted changes (staged,
// unstaged, or untracked).
func srcDirIsDirty(repoDir, srcDir string) bool {
	out, err := exec.Command("git", "-C", repoDir,
		"status", "--porcelain", "--", srcDir).Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}

// checkImageSkews assesses every locally-built image against the repo.
// Returns nil when the repo state is unreadable (git missing, not a
// checkout): no signal beats a wrong warning.
func checkImageSkews(c *Compose, repoDir string) []skewReport {
	if err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Run(); err != nil {
		return nil
	}
	reports := make([]skewReport, 0, len(skewServices))
	for _, s := range skewServices {
		// Compose names locally-built images {project}-{service}.
		image := c.projectName + "-" + s.service
		label, labelErr := imageVersionLabel(c, image)
		var behind int
		var behindErr error
		dirty := false
		if labelErr == nil && label != "" && label != "dev" {
			behind, behindErr = commitsBehind(repoDir, extractCommit(label), s.srcDir)
			dirty = srcDirIsDirty(repoDir, s.srcDir)
		}
		reports = append(reports, classifySkew(s.service, s.srcDir, label, labelErr, behind, behindErr, dirty))
	}
	return reports
}

var (
	gitVersionOnce   sync.Once
	gitVersionCached string
)

// cachedGitVersion memoizes gitDescribe for the repo root: dockerCmd calls
// it on every docker invocation and some commands issue several.
func cachedGitVersion(repoDir string) string {
	gitVersionOnce.Do(func() { gitVersionCached = gitDescribe(repoDir) })
	return gitVersionCached
}
