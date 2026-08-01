package roleenhance

import (
	"fmt"
	"strings"
	"testing"
)

func TestBoundAdvisorEvidenceEnforcesGlobalLimitsAndKeepsPriorityInputs(t *testing.T) {
	evidence := Evidence{}
	for i := 0; i < 200; i++ {
		path := fmt.Sprintf("modules/module-%03d", i)
		evidence.Sources = append(evidence.Sources, EvidenceSource{Kind: EvidenceRepository, Path: path + "/package.json", Excerpt: strings.Repeat("x", 40)})
		evidence.Facts = append(evidence.Facts, EvidenceFact{Kind: EvidenceRepository, Path: path, Key: "repository_directory", Value: path})
	}
	evidence.Sources = append(evidence.Sources,
		EvidenceSource{Kind: EvidenceRepository, Path: "go.mod", Excerpt: "module example"},
		EvidenceSource{Kind: EvidenceRepository, Path: ".github/workflows/ci.yml", Excerpt: "go test ./..."},
	)
	evidence.Facts = append(evidence.Facts,
		EvidenceFact{Kind: EvidenceRepository, Path: "go.mod", Key: "repository_input", Value: "build_configuration"},
		EvidenceFact{Kind: EvidenceRepository, Path: "go.mod", Key: "repository_input", Value: "build_configuration"},
	)

	bounded := boundAdvisorEvidence(evidence, CollectorLimits{MaxFiles: 8, MaxFileBytes: 50, MaxTotalBytes: 100})
	if len(bounded.Sources) > 8 || len(bounded.Facts) > 8 || bounded.TotalBytes > 100 {
		t.Fatalf("global limits exceeded: sources=%d facts=%d bytes=%d", len(bounded.Sources), len(bounded.Facts), bounded.TotalBytes)
	}
	for _, path := range []string{".github/workflows/ci.yml", "go.mod"} {
		if !evidenceHasPath(bounded, path) {
			t.Fatalf("priority evidence %q was dropped: %+v", path, bounded)
		}
	}
	if got := countFact(bounded.Facts, "go.mod", "repository_input", "build_configuration"); got != 1 {
		t.Fatalf("deduplicated go.mod facts = %d, want 1", got)
	}
}

func TestBoundAdvisorEvidenceTruncatesOnUTF8Boundary(t *testing.T) {
	evidence := Evidence{Sources: []EvidenceSource{{Path: "README.md", Excerpt: "abc🙂def"}}}
	bounded := boundAdvisorEvidence(evidence, CollectorLimits{MaxFiles: 1, MaxTotalBytes: 5})
	if len(bounded.Sources) != 1 || bounded.Sources[0].Excerpt != "abc" || !bounded.Sources[0].Truncated {
		t.Fatalf("bounded UTF-8 evidence = %+v", bounded)
	}
}

func TestBoundAdvisorEvidencePrioritizesNestedBuildFiles(t *testing.T) {
	evidence := Evidence{Sources: []EvidenceSource{
		{Kind: EvidenceDocumentation, Path: "docs/a.md", Excerpt: "docs"},
		{Kind: EvidenceDocumentation, Path: "docs/b.md", Excerpt: "docs"},
		{Kind: EvidenceRepository, Path: "mobile-android/app/build.gradle.kts", Excerpt: "androidx.compose"},
	}}
	bounded := boundAdvisorEvidence(evidence, CollectorLimits{MaxFiles: 1, MaxTotalBytes: 100})
	if len(bounded.Sources) != 1 || bounded.Sources[0].Path != "mobile-android/app/build.gradle.kts" {
		t.Fatalf("nested build file was not prioritized: %+v", bounded.Sources)
	}
}

func countFact(facts []EvidenceFact, path, key, value string) int {
	count := 0
	for _, fact := range facts {
		if fact.Path == path && fact.Key == key && fact.Value == value {
			count++
		}
	}
	return count
}
