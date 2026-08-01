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
		"substring": {`{"schema_version":"promptgrinder.role-advisor/v1","recommendations":[{"id":"x","role_id":"backend","operation":"append","field":"technology","value":"ring","confidence":"high","explanation":"use it","evidence":[{"path":"README.md"}]}]}`, "ungrounded technology"},
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

func TestValidateRecommendationsAllowsGoGroundedByCollectedModule(t *testing.T) {
	current, evidence := advisorFixture()
	current.Project.Languages = nil
	for i := range current.Roles {
		current.Roles[i].Technology = nil
	}
	recommendation := Recommendation{ID: "go", RoleID: "backend", Field: "technology", Operation: OperationAppend, Value: "Go", Confidence: ConfidenceHigh, Explanation: "go.mod identifies the repository language", Evidence: []Citation{{Path: "go.mod"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("go.mod-grounded technology rejected: %v", err)
	}

	evidence.Sources = []EvidenceSource{{Kind: EvidenceDocumentation, Path: "README.md", Excerpt: "mentions Go without a module"}}
	evidence.Facts = nil
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err == nil || !strings.Contains(err.Error(), "ungrounded technology") {
		t.Fatalf("technology without go.mod evidence error = %v", err)
	}
}

func TestValidateRecommendationsAllowsGroundedMultiValueAppend(t *testing.T) {
	current, evidence := advisorFixture()
	recommendation := Recommendation{ID: "gates", RoleID: "backend", Field: "quality_gates", Operation: OperationAppend, Value: []any{"go test ./...", "go test"}, Confidence: ConfidenceHigh, Explanation: "repository evidence contains both commands", Evidence: []Citation{{Path: "README.md"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("grounded multi-value append rejected: %v", err)
	}
	recommendation.Operation = OperationRemove
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("grounded multi-value removal rejected: %v", err)
	}
}

func TestValidateRecommendationsGroundsQuotedCommandAgainstRawEvidence(t *testing.T) {
	current, evidence := advisorFixture()
	evidence.Sources = append(evidence.Sources, EvidenceSource{Kind: EvidenceRepository, Path: ".github/workflows/ci.yml", Excerpt: `run: test -z "$(gofmt -l .)"`})
	recommendation := Recommendation{ID: "format", RoleID: "backend", Field: "quality_gates", Operation: OperationAppend, Value: `test -z "$(gofmt -l .)"`, Confidence: ConfidenceHigh, Explanation: "exact CI command", Evidence: []Citation{{Path: ".github/workflows/ci.yml"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("quoted command from raw evidence rejected: %v", err)
	}
}

func TestValidateRecommendationsGroundsMultilineCommandAcrossIndentation(t *testing.T) {
	current, evidence := advisorFixture()
	evidence.Sources = append(evidence.Sources, EvidenceSource{Kind: EvidenceRepository, Path: ".github/workflows/backend-ci.yml", Excerpt: "run: |\n          ./mvnw -B verify \\\n            -Dsonar.token=\"$SONAR_TOKEN\" \\\n            -Dsonar.projectKey=footybadger"})
	recommendation := Recommendation{ID: "sonar", RoleID: "backend", Field: "quality_gates", Operation: OperationAppend, Value: "./mvnw -B verify \\\n  -Dsonar.token=\"$SONAR_TOKEN\" \\\n  -Dsonar.projectKey=footybadger", Confidence: ConfidenceHigh, Explanation: "exact workflow command with presentation-only indentation changes", Evidence: []Citation{{Path: ".github/workflows/backend-ci.yml"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("multiline command with equivalent whitespace rejected: %v", err)
	}
}

func TestValidateRecommendationsGroundsMultilineCommandFromExactCollectedFragments(t *testing.T) {
	current, evidence := advisorFixture()
	evidence.Sources = append(evidence.Sources,
		EvidenceSource{Kind: EvidenceRepository, Path: ".github/workflows/android-ci.yml", Excerpt: "run: |\n  cd mobile-android\n  ./gradlew \\\n    :app:testLocalDebugUnitTest \\\n    :app:lintLocalDebug"},
		EvidenceSource{Kind: EvidenceDocumentation, Path: "README.md", Excerpt: "./gradlew \\\n  :app:compileLocalDebugKotlin \\\n  :app:testLocalDebugUnitTest \\\n  :app:lintLocalDebug"},
	)
	recommendation := Recommendation{ID: "android", RoleID: "backend", Field: "quality_gates", Operation: OperationAppend, Value: "cd mobile-android\n./gradlew \\\n  :app:compileLocalDebugKotlin \\\n  :app:testLocalDebugUnitTest \\\n  :app:lintLocalDebug", Confidence: ConfidenceHigh, Explanation: "every command fragment is collected", Evidence: []Citation{{Path: ".github/workflows/android-ci.yml"}, {Path: "README.md"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("command composed from exact collected lines rejected: %v", err)
	}
	recommendation.Value = recommendation.Value.(string) + " \\\n  :app:inventedTask"
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err == nil || !strings.Contains(err.Error(), "ungrounded quality_gates") {
		t.Fatalf("command with invented fragment error = %v", err)
	}
}

func TestValidateRecommendationsGroundsDeepPathMentionedInEvidence(t *testing.T) {
	current, evidence := advisorFixture()
	evidence.Sources = append(evidence.Sources, EvidenceSource{Kind: EvidenceDocumentation, Path: "docs/testing.md", Excerpt: "Local Android tests live under `mobile-android/app/src/test/java`."})
	recommendation := Recommendation{ID: "android-tests", RoleID: "backend", Field: "allowed_paths", Operation: OperationAppend, Value: "mobile-android/app/src/test", Confidence: ConfidenceHigh, Explanation: "testing guide identifies the test tree", Evidence: []Citation{{Path: "docs/testing.md"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("deep path mentioned by collected evidence rejected: %v", err)
	}
	recommendation.Value = "mobile-android/app/src/invented"
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err == nil || !strings.Contains(err.Error(), "ungrounded context path") {
		t.Fatalf("invented deep path error = %v", err)
	}
}

func TestValidateRecommendationsGroundsComposeFromCollectedBuildEvidence(t *testing.T) {
	current, evidence := advisorFixture()
	evidence.Sources = append(evidence.Sources, EvidenceSource{Kind: EvidenceRepository, Path: "mobile-android/app/build.gradle.kts", Excerpt: `buildFeatures { compose = true } dependencies { implementation("androidx.compose.ui:ui") }`})
	recommendation := Recommendation{ID: "compose", RoleID: "backend", Field: "technology", Operation: OperationAppend, Value: "Jetpack Compose", Confidence: ConfidenceHigh, Explanation: "Gradle explicitly enables Compose", Evidence: []Citation{{Path: "mobile-android/app/build.gradle.kts"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("Compose from collected Gradle evidence rejected: %v", err)
	}
}

func TestValidateRecommendationsGroundsExplicitJavaVersionFromBuildEvidence(t *testing.T) {
	current, evidence := advisorFixture()
	evidence.Sources = append(evidence.Sources, EvidenceSource{Kind: EvidenceRepository, Path: "backend/pom.xml", Excerpt: `<properties><java.version>21</java.version></properties>`})
	recommendation := Recommendation{ID: "java", RoleID: "backend", Field: "technology", Operation: OperationAppend, Value: "Java 21", Confidence: ConfidenceHigh, Explanation: "Maven declares Java 21", Evidence: []Citation{{Path: "backend/pom.xml"}}}
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err != nil {
		t.Fatalf("declared Java version rejected: %v", err)
	}
	recommendation.Value = "Java 22"
	if err := ValidateRecommendations(current, evidence, []Recommendation{recommendation}); err == nil || !strings.Contains(err.Error(), "ungrounded technology") {
		t.Fatalf("undeclared Java version error = %v", err)
	}
}
