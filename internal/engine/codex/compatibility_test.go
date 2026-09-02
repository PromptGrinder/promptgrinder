package codex

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const initialHelp = "Commands: exec\nOptions: --cd <DIR> --sandbox <SANDBOX_MODE> --json --dangerously-bypass-approvals-and-sandbox"
const resumeHelp = "Commands: resume\nOptions: --config <key=value> --json --dangerously-bypass-approvals-and-sandbox"

func TestAssessVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		status VersionStatus
	}{
		{name: "known qualified release", output: "codex-cli 0.150.1", status: VersionQualified},
		{name: "known qualified patch", output: "codex 0.150.99\n", status: VersionQualified},
		{name: "newer minor is provisional", output: "codex-cli 0.152.0", status: VersionProvisional},
		{name: "older minor is provisional", output: "codex-cli 0.149.9", status: VersionProvisional},
		{name: "malformed output", output: "Codex development build", status: VersionMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AssessVersion(test.output); got.Status != test.status {
				t.Fatalf("AssessVersion(%q) = %#v, want status %q", test.output, got, test.status)
			}
		})
	}
}

func TestValidateInstalledVersionSkipsProbeForKnownQualifiedRelease(t *testing.T) {
	var invocations [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		invocations = append(invocations, append([]string(nil), args...))
		return []byte("codex-cli 0.150.1"), nil
	}
	assessment, err := ValidateInstalledVersion(context.Background(), "/test/codex", run)
	if err != nil || assessment.Status != VersionQualified {
		t.Fatalf("ValidateInstalledVersion() = %#v, %v", assessment, err)
	}
	if want := [][]string{{"--version"}}; !reflect.DeepEqual(invocations, want) {
		t.Fatalf("invocations = %#v, want %#v", invocations, want)
	}
}

func TestValidateInstalledVersionAllowsNewerReleaseWhenAdapterProbePasses(t *testing.T) {
	var invocations [][]string
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		invocations = append(invocations, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"--version"}):
			return []byte("codex-cli 0.152.0"), nil
		case reflect.DeepEqual(args, []string{"exec", "--help"}):
			return []byte(initialHelp), nil
		case reflect.DeepEqual(args, []string{"exec", "resume", "--help"}):
			return []byte(resumeHelp), nil
		default:
			return nil, errors.New("unexpected invocation")
		}
	}
	assessment, err := ValidateInstalledVersion(context.Background(), "/test/codex", run)
	if err != nil || assessment.Status != VersionProvisional {
		t.Fatalf("ValidateInstalledVersion() = %#v, %v", assessment, err)
	}
	if !strings.Contains(assessment.Reason, "passed") {
		t.Fatalf("reason = %q", assessment.Reason)
	}
	want := [][]string{{"--version"}, {"exec", "--help"}, {"exec", "resume", "--help"}}
	if !reflect.DeepEqual(invocations, want) {
		t.Fatalf("invocations = %#v, want %#v", invocations, want)
	}
}

func TestValidateInstalledVersionRejectsNewerReleaseWhenAdapterProbeFails(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"--version"}) {
			return []byte("codex-cli 0.152.0"), nil
		}
		return []byte("Usage: codex exec --json"), nil
	}
	_, err := ValidateInstalledVersion(context.Background(), "codex", run)
	if err == nil || !strings.Contains(err.Error(), "capability probe failed") || !strings.Contains(err.Error(), "--sandbox") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateInstalledVersionProbesUnparseableVersion(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case reflect.DeepEqual(args, []string{"--version"}):
			return []byte("Codex development build"), nil
		case reflect.DeepEqual(args, []string{"exec", "--help"}):
			return []byte(initialHelp), nil
		default:
			return []byte(resumeHelp), nil
		}
	}
	assessment, err := ValidateInstalledVersion(context.Background(), "codex", run)
	if err != nil || assessment.Status != VersionProvisional {
		t.Fatalf("ValidateInstalledVersion() = %#v, %v", assessment, err)
	}
}

func TestValidateInstalledVersionReportsVersionCommandFailure(t *testing.T) {
	_, err := ValidateInstalledVersion(context.Background(), "codex", func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not executable")
	})
	if err == nil || !strings.Contains(err.Error(), "read Codex CLI version") {
		t.Fatalf("error = %v", err)
	}
}
