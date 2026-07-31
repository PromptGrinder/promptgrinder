package roleenhance

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectorsDiscoveredProjectAndStableOrdering(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project with spaces")
	writeFiles(t, root, map[string]string{
		".promptgrinder/project.yaml":       "name: example\nlanguages: [Go]\nframeworks: []\nroles: [backend]\ngenerated_by: promptgrinder discover\n",
		".promptgrinder/roles/backend.yaml": "id: backend\nname: Backend\ndescription: work\ntechnology: [Go]\nallowed_paths: [internal]\nruntime: {preferred: local}\nquality_gates: [go test ./...]\nstatus: {generated: true}\n",
		"README.md":                         "# Example\n", "docs/testing.md": "Run tests\n", "go.mod": "module example\n", ".github/workflows/ci.yml": "jobs: {}\n",
		".promptgrinder/context/team.md": "team context\n", "skills/review/SKILL.md": "review carefully\n", ".ai/instructions.md": "public instructions\n",
	})
	state, err := LoadCurrentState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Project.Name != "example" || len(state.Roles) != 1 || state.Roles[0].ID != "backend" {
		t.Fatalf("state = %+v", state)
	}
	repo1, err := (RepositoryContextCollector{}).Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	repo2, _ := (RepositoryContextCollector{}).Collect(root)
	if !reflect.DeepEqual(repo1, repo2) {
		t.Fatal("repository evidence is unstable")
	}
	if got := sourcePaths(repo1); !reflect.DeepEqual(got, []string{".github/workflows/ci.yml", "go.mod"}) {
		t.Fatalf("repository paths = %v", got)
	}
	docs, err := (DocumentationCollector{}).Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := sourcePaths(docs); !reflect.DeepEqual(got, []string{"README.md", "docs/testing.md"}) {
		t.Fatalf("documentation paths = %v", got)
	}
	skills, err := (SkillCollector{}).Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := sourcePaths(skills); !reflect.DeepEqual(got, []string{".ai/instructions.md", ".promptgrinder/context/team.md", "skills/review/SKILL.md"}) {
		t.Fatalf("skill paths = %v", got)
	}
}

func TestCollectorsAllowMissingOptionalDirectories(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"README.md": "ok"})
	for _, collect := range []func(string) (Evidence, error){(RepositoryContextCollector{}).Collect, (SkillCollector{}).Collect} {
		e, err := collect(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(e.Sources) != 0 {
			t.Fatalf("unexpected evidence: %+v", e)
		}
	}
}

func TestCollectorLimitsAreHardAndDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"docs/a.md": "1234567890", "docs/b.md": "abcdefghij", "docs/c.md": "more"})
	c := DocumentationCollector{Limits: CollectorLimits{MaxFiles: 2, MaxFileBytes: 5, MaxTotalBytes: 8}}
	e, err := c.Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Sources) != 2 || e.TotalBytes != 8 || e.Sources[0].Excerpt != "12345" || e.Sources[1].Excerpt != "abc" || !e.Sources[0].Truncated {
		t.Fatalf("bounded evidence = %+v", e)
	}
}

func TestCollectorsSkipSymlinksBinariesAndSecrets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{"escape.md": "outside"})
	writeFiles(t, root, map[string]string{
		"README.md": "safe\nAPI_KEY=do-not-collect\nmore safe", ".env": "PASSWORD=x", "docs/secret-notes.md": "no", ".ai/token.txt": "no", "docs/image.bin": string([]byte{1, 0, 2}),
	})
	if err := os.Symlink(filepath.Join(outside, "escape.md"), filepath.Join(root, "docs", "linked.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	docs, err := (DocumentationCollector{}).Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, s := range docs.Sources {
		joined += s.Path + s.Excerpt
	}
	if strings.Contains(joined, "do-not-collect") || strings.Contains(joined, "outside") || strings.Contains(joined, "secret-notes") || strings.Contains(joined, "image.bin") {
		t.Fatalf("unsafe evidence collected: %q", joined)
	}
	skills, err := (SkillCollector{}).Collect(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills.Sources) != 0 {
		t.Fatalf("secret/symlink skill evidence: %+v", skills)
	}
}

func TestLoadCurrentStateRejectsMalformedYAML(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{".promptgrinder/project.yaml": "name: [\n"})
		if _, err := LoadCurrentState(root); err == nil || !strings.Contains(err.Error(), "project.yaml") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("role", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{".promptgrinder/project.yaml": "name: x\nroles: [bad]\n", ".promptgrinder/roles/bad.yaml": "id: [\n"})
		if _, err := LoadCurrentState(root); err == nil || !strings.Contains(err.Error(), "bad.yaml") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestStableReviewPlan(t *testing.T) {
	items := []ReviewItem{{Recommendation: Recommendation{ID: "b", RoleID: "z", Operation: OperationSet, Field: "description", Value: "x", Confidence: ConfidenceHigh, Explanation: "why", Evidence: []Citation{{Path: "README.md"}}}}, {Recommendation: Recommendation{ID: "a", RoleID: "a", Operation: OperationAppend, Field: "quality_gates", Value: "test", Confidence: ConfidenceMedium, Explanation: "why", Evidence: []Citation{{Path: "go.mod"}}}}}
	p, err := StableReviewPlan(items)
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].Recommendation.ID != "a" {
		t.Fatalf("unstable plan: %+v", p)
	}
}

func TestRecommendationEvidenceMustHaveBeenCollected(t *testing.T) {
	evidence := Evidence{Sources: []EvidenceSource{{Path: "README.md"}}, Facts: []EvidenceFact{{Path: "go.mod", Key: "repository_input", Value: "build_configuration"}}}
	valid := Recommendation{ID: "valid", Evidence: []Citation{{Path: "go.mod", Fact: "repository_input=build_configuration"}}}
	if err := ValidateRecommendationEvidence([]Recommendation{valid}, evidence); err != nil {
		t.Fatal(err)
	}
	invalid := Recommendation{ID: "invalid", Evidence: []Citation{{Path: "missing.md"}}}
	if err := ValidateRecommendationEvidence([]Recommendation{invalid}, evidence); err == nil {
		t.Fatal("expected uncollected citation to be rejected")
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
func sourcePaths(e Evidence) []string {
	out := make([]string, len(e.Sources))
	for i := range e.Sources {
		out[i] = e.Sources[i].Path
	}
	return out
}
