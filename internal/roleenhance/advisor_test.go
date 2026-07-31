package roleenhance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"promptgrinder/internal/workerlaunch"
)

type fakeAdvisorRuntime struct {
	capabilities workerlaunch.Capabilities
	response     []byte
	err          error
	requests     [][]byte
}

func (f *fakeAdvisorRuntime) Capabilities() workerlaunch.Capabilities { return f.capabilities }
func (f *fakeAdvisorRuntime) Advise(_ context.Context, request []byte) ([]byte, error) {
	f.requests = append(f.requests, append([]byte(nil), request...))
	return append([]byte(nil), f.response...), f.err
}

func capableFake(response string) *fakeAdvisorRuntime {
	return &fakeAdvisorRuntime{capabilities: workerlaunch.Capabilities{Headless: true, StructuredOutput: true, Sandbox: true}, response: []byte(response)}
}

func advisorFixture() (CurrentState, Evidence) {
	return CurrentState{
			Project: ProjectSnapshot{Name: "example", Languages: []string{"Go"}, Roles: []string{"backend"}},
			Roles:   []RoleSnapshot{{ID: "backend", Name: "Backend", Description: "old", Technology: []string{"Go"}, AllowedPaths: []string{"internal"}, Runtime: RuntimeSnapshot{Preferred: "local"}, QualityGates: []string{"go test ./..."}}},
		}, Evidence{
			Sources: []EvidenceSource{{Kind: EvidenceDocumentation, Path: "README.md", Excerpt: "Backend services. Run go test ./... before review."}, {Kind: EvidenceRepository, Path: "go.mod", Excerpt: "module example"}},
			Facts:   []EvidenceFact{{Kind: EvidenceRepository, Path: "internal", Key: "repository_directory", Value: "internal"}},
		}
}

func TestAiRoleAdvisorReturnsStableValidatedPlan(t *testing.T) {
	current, evidence := advisorFixture()
	runtime := capableFake(`{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[{"id":"gate","role_id":"backend","operation":"append","field":"quality_gates","value":"go test ./...","confidence":"high","explanation":"CI-compatible check","evidence":[{"path":"README.md"}]},{"id":"desc","role_id":"backend","operation":"set","field":"description","value":"Own backend services","confidence":"medium","explanation":"The documentation assigns backend services","evidence":[{"path":"README.md"}]}]}`)
	plan, err := (AiRoleAdvisor{Runtime: runtime}).Recommend(context.Background(), current, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{plan.Items[0].Recommendation.ID, plan.Items[1].Recommendation.ID}; !reflect.DeepEqual(got, []string{"desc", "gate"}) {
		t.Fatalf("recommendation order = %v", got)
	}
	if plan.Items[0].OldValue != "old" || len(runtime.requests) != 1 {
		t.Fatalf("plan/runtime = %#v/%d", plan, len(runtime.requests))
	}
}

func TestAdvisorRequestIsBoundedToDataAndDeterministicallyOrdered(t *testing.T) {
	current, evidence := advisorFixture()
	current.Roles = append([]RoleSnapshot{{ID: "z", Technology: []string{"Z", "A"}}}, current.Roles...)
	evidence.Sources = append([]EvidenceSource{{Path: "z.md", Excerpt: "z"}}, evidence.Sources...)
	a, err := BuildAdvisorRequest(current, evidence)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildAdvisorRequest(current, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || strings.Index(string(a), `"id":"backend"`) > strings.Index(string(a), `"id":"z"`) || strings.Contains(string(a), "source_path") {
		t.Fatalf("request is not normalized: %s", a)
	}
}

func TestAiRoleAdvisorRejectsUngroundedTechnologyAndFabricatedEvidence(t *testing.T) {
	current, evidence := advisorFixture()
	for name, test := range map[string]struct{ response, want string }{
		"framework": {`{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[{"id":"x","role_id":"backend","operation":"append","field":"technology","value":"Django","confidence":"high","explanation":"use it","evidence":[{"path":"README.md"}]}]}`, "ungrounded technology"},
		"evidence":  {`{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[{"id":"x","role_id":"backend","operation":"set","field":"description","value":"backend","confidence":"high","explanation":"use it","evidence":[{"path":"invented.md"}]}]}`, "uncollected path"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (AiRoleAdvisor{Runtime: capableFake(test.response)}).Recommend(context.Background(), current, evidence)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestAiRoleAdvisorSurfacesMalformedTimeoutFailureAndCapabilities(t *testing.T) {
	current, evidence := advisorFixture()
	tests := []struct {
		name    string
		runtime *fakeAdvisorRuntime
		want    string
	}{
		{"malformed", capableFake(`not-json`), "invalid role advisor JSON/schema"},
		{"timeout", &fakeAdvisorRuntime{capabilities: capableFake("").capabilities, err: context.DeadlineExceeded}, "timed out"},
		{"failure", &fakeAdvisorRuntime{capabilities: capableFake("").capabilities, err: errors.New("boom")}, "runtime failed"},
		{"capability", &fakeAdvisorRuntime{}, "lacks required capabilities"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (AiRoleAdvisor{Runtime: test.runtime}).Recommend(context.Background(), current, evidence)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestAdvisorRuntimeSelectionIsConfigurable(t *testing.T) {
	runtime := capableFake(`{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[]}`)
	var registry AdvisorRuntimeRegistry
	if err := registry.Register("test", runtime); err != nil {
		t.Fatal(err)
	}
	advisor, err := registry.Advisor("test")
	if err != nil || advisor.Runtime != runtime {
		t.Fatalf("advisor = %#v, err = %v", advisor, err)
	}
	if _, err := registry.Advisor("codex"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("missing runtime error = %v", err)
	}
}

func TestAiRoleAdvisorDoesNotWriteProjectFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".promptgrinder", "roles", "backend.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	before := []byte("id: backend\n")
	if err := os.WriteFile(path, before, 0o444); err != nil {
		t.Fatal(err)
	}
	current, evidence := advisorFixture()
	runtime := capableFake(`{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[]}`)
	if _, err := (AiRoleAdvisor{Runtime: runtime}).Recommend(context.Background(), current, evidence); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("role changed: %q, %v", after, err)
	}
}

func TestValidateRecommendationsAllowsGroundedRemovalAndRejectsSecrets(t *testing.T) {
	current, evidence := advisorFixture()
	base := Recommendation{ID: "x", RoleID: "backend", Field: "technology", Operation: OperationRemove, Value: "Go", Confidence: ConfidenceHigh, Explanation: "cleanup", Evidence: []Citation{{Path: "go.mod"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{base}); err != nil {
		t.Fatalf("err = %v", err)
	}
	base.Operation, base.Field, base.Value = OperationSet, "description", "API_KEY=supersecretvalue"
	if err := ValidateRecommendations(current, evidence, []Recommendation{base}); err == nil || !strings.Contains(err.Error(), "secret-looking") {
		t.Fatalf("err = %v", err)
	}
}
