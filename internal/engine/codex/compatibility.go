package codex

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`(?i)\bcodex(?:-cli)?\s+v?(\d+)\.(\d+)\.(\d+)(?:[-+][^\s]+)?`)

type VersionStatus string

const (
	VersionQualified VersionStatus = "qualified"
	// VersionProvisional means the release is outside the known-qualified band.
	// It may still run after the adapter capability probe succeeds.
	VersionProvisional VersionStatus = "provisional"
	VersionMalformed   VersionStatus = "malformed"
)

type VersionAssessment struct {
	Raw     string
	Version string
	Status  VersionStatus
	Reason  string
}

// AssessVersion recognizes the Codex CLI version format and identifies the
// known-qualified compatibility band. A version outside that band is not
// rejected by version alone: ValidateInstalledVersion verifies the command
// contract before allowing it to run provisionally.
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
	return VersionAssessment{Raw: raw, Version: version, Status: VersionProvisional, Reason: "Codex CLI version is outside PromptGrinder's known-qualified 0.150.x band; PromptGrinder will verify the required adapter capabilities before launch"}
}

var requiredInitialCapabilities = []string{
	"exec",
	"--cd",
	"--sandbox",
	"--json",
	"--dangerously-bypass-approvals-and-sandbox",
}

var requiredResumeCapabilities = []string{
	"resume",
	"--config",
	"--json",
	"--dangerously-bypass-approvals-and-sandbox",
}

// ProbeInstalledCLI verifies the non-mutating Codex command-line capabilities
// that the adapter needs for new and resumed workers. It deliberately uses
// help commands only: no repository, session, prompt, or model request is
// created during preflight.
func ProbeInstalledCLI(ctx context.Context, executable string, run func(context.Context, string, ...string) ([]byte, error)) error {
	initial, err := run(ctx, executable, "exec", "--help")
	if err != nil {
		return fmt.Errorf("read Codex exec help: %w", err)
	}
	if err := requireCapabilities(string(initial), requiredInitialCapabilities); err != nil {
		return fmt.Errorf("Codex exec capability probe: %w", err)
	}
	resume, err := run(ctx, executable, "exec", "resume", "--help")
	if err != nil {
		return fmt.Errorf("read Codex exec resume help: %w", err)
	}
	if err := requireCapabilities(string(resume), requiredResumeCapabilities); err != nil {
		return fmt.Errorf("Codex exec resume capability probe: %w", err)
	}
	return nil
}

func requireCapabilities(help string, required []string) error {
	missing := make([]string, 0)
	for _, capability := range required {
		if !strings.Contains(help, capability) {
			missing = append(missing, capability)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required option(s): %s", strings.Join(missing, ", "))
	}
	return nil
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
	if assessment.Status == VersionQualified {
		return assessment, nil
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer probeCancel()
	if err := ProbeInstalledCLI(probeCtx, executable, run); err != nil {
		return assessment, fmt.Errorf("%s; required adapter capability probe failed: %w", assessment.Reason, err)
	}
	assessment.Status = VersionProvisional
	assessment.Reason = "Codex CLI is outside PromptGrinder's known-qualified 0.150.x band but passed the required non-mutating adapter capability probe"
	return assessment, nil
}
