package roleenhance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"promptgrinder/internal/testsupport"
)

func TestAdvisorResponseSchemaRequiresEveryCitationProperty(t *testing.T) {
	var schema struct {
		Properties struct {
			Recommendations struct {
				Items struct {
					Properties struct {
						Evidence struct {
							Items struct {
								Required []string `json:"required"`
							} `json:"items"`
						} `json:"evidence"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"recommendations"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(advisorResponseJSONSchema), &schema); err != nil {
		t.Fatalf("parse advisor response schema: %v", err)
	}
	required := schema.Properties.Recommendations.Items.Properties.Evidence.Items.Required
	if len(required) != 2 || required[0] != "path" || required[1] != "fact" {
		t.Fatalf("citation required properties = %v, want [path fact]", required)
	}
}

func TestCodexRuntimeSurfacesSanitizedStderr(t *testing.T) {
	fake := testsupport.FakeExecutable(t, "codex", "#!/bin/sh\nprintf '%s\\n' 'invalid output schema: fact must be required' >&2\nprintf '%s\\n' 'api_key=do-not-print' >&2\nexit 17\n")

	_, err := (CodexRuntime{Executable: fake}).Advise(context.Background(), []byte(`{"schema_version":"promptgrinder.role-advisor/v1"}`))
	if err == nil {
		t.Fatal("Advise() error = nil, want execution failure")
	}
	message := err.Error()
	if !strings.Contains(message, "exit status 17") || !strings.Contains(message, "fact must be required") {
		t.Fatalf("Advise() error = %q, want exit status and Codex diagnostic", message)
	}
	if strings.Contains(message, "do-not-print") {
		t.Fatalf("Advise() error leaked secret: %q", message)
	}
}
