package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const AllowUnqualifiedVersionEnvironment = "PROMPTGRINDER_ALLOW_UNQUALIFIED_CODEX_VERSION"

var versionPattern = regexp.MustCompile(`(?i)\bcodex(?:-cli)?\s+v?(\d+)\.(\d+)\.(\d+)(?:[-+][^\s]+)?`)

type VersionStatus string

const (
	VersionQualified   VersionStatus = "qualified"
	VersionUnqualified VersionStatus = "unqualified"
	VersionMalformed   VersionStatus = "malformed"
)

type VersionAssessment struct {
	Raw     string
	Version string
	Status  VersionStatus
	Reason  string
}

// AssessVersion recognizes the Codex CLI version format and applies the RC.6.1
// qualified band. Newer or older minor versions are intentionally not assumed
// compatible merely because they parse as semantic versions.
func AssessVersion(output string) VersionAssessment {
	raw := strings.TrimSpace(output)
	match := versionPattern.FindStringSubmatch(raw)
	if len(match) != 4 {
		return VersionAssessment{Raw: raw, Status: VersionMalformed, Reason: "could not parse a Codex CLI semantic version"}
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	version := fmt.Sprintf("%d.%d.%d", major, minor, patch)
	if major == 0 && minor == 150 {
		return VersionAssessment{Raw: raw, Version: version, Status: VersionQualified}
	}
	return VersionAssessment{Raw: raw, Version: version, Status: VersionUnqualified, Reason: "PromptGrinder RC.6.1 qualifies Codex CLI 0.150.x; this version needs explicit opt-in until qualified"}
}

func AllowUnqualifiedVersion() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(AllowUnqualifiedVersionEnvironment)))
	return value == "1" || value == "true" || value == "yes"
}

func ValidateInstalledVersion(ctx context.Context, executable string, run func(context.Context, string, ...string) ([]byte, error)) (VersionAssessment, error) {
	if run == nil {
		run = func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, executable, args...).Output()
		}
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := run(versionCtx, executable, "--version")
	if err != nil {
		return VersionAssessment{}, fmt.Errorf("read Codex CLI version: %w", err)
	}
	assessment := AssessVersion(string(output))
	if assessment.Status == VersionQualified || AllowUnqualifiedVersion() {
		return assessment, nil
	}
	return assessment, fmt.Errorf("%s; set %s=1 to explicitly run this unqualified Codex CLI release", assessment.Reason, AllowUnqualifiedVersionEnvironment)
}
