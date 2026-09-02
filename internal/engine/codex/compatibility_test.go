package codex

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAssessVersion(t *testing.T) {
	tests := []struct {
		name   string
		output string
		status VersionStatus
	}{
		{name: "qualified release", output: "codex-cli 0.150.1", status: VersionQualified},
		{name: "qualified patch", output: "codex 0.150.99\n", status: VersionQualified},
		{name: "newer minor is unqualified", output: "codex-cli 0.151.0", status: VersionUnqualified},
		{name: "older minor is unqualified", output: "codex-cli 0.149.9", status: VersionUnqualified},
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

func TestValidateInstalledVersion(t *testing.T) {
	t.Setenv(AllowUnqualifiedVersionEnvironment, "")
	var executable string
	var arguments []string
	run := func(_ context.Context, binary string, args ...string) ([]byte, error) {
		executable, arguments = binary, append([]string(nil), args...)
		return []byte("codex-cli 0.150.1"), nil
	}
	assessment, err := ValidateInstalledVersion(context.Background(), "/test/codex", run)
	if err != nil || assessment.Status != VersionQualified {
		t.Fatalf("ValidateInstalledVersion() = %#v, %v", assessment, err)
	}
	if executable != "/test/codex" || len(arguments) != 1 || arguments[0] != "--version" {
		t.Fatalf("version invocation = %q %#v", executable, arguments)
	}
}

func TestValidateInstalledVersionRejectsUnqualifiedWithoutExplicitOverride(t *testing.T) {
	t.Setenv(AllowUnqualifiedVersionEnvironment, "")
	_, err := ValidateInstalledVersion(context.Background(), "codex", func(context.Context, string, ...string) ([]byte, error) {
		return []byte("codex-cli 0.151.0"), nil
	})
	if err == nil || !strings.Contains(err.Error(), AllowUnqualifiedVersionEnvironment+"=1") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateInstalledVersionAllowsUnqualifiedWithExplicitOverride(t *testing.T) {
	t.Setenv(AllowUnqualifiedVersionEnvironment, "true")
	assessment, err := ValidateInstalledVersion(context.Background(), "codex", func(context.Context, string, ...string) ([]byte, error) {
		return []byte("codex-cli 0.151.0"), nil
	})
	if err != nil || assessment.Status != VersionUnqualified {
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
