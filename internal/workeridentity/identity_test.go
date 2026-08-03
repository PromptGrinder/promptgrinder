package workeridentity

import (
	"testing"

	"promptgrinder/internal/state"
)

func TestFromWorkerReportsEffectiveEngineModelAndSlicePolicy(t *testing.T) {
	identity := FromWorker(state.Worker{
		Engine: "codex",
		ResolvedMetadata: map[string]any{
			"allowed_paths": []any{"backend/**"},
			"engine":        map[string]any{"name": "codex", "model": "gpt-5.6-sol"},
		},
	})
	if identity.Scope != "slice-policy" || identity.Engine != "codex" || identity.Model != "gpt-5.6-sol" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestFromWorkerPrefersDeclaredRoleOverSlicePolicyLabel(t *testing.T) {
	identity := FromWorker(state.Worker{
		Engine: "codex",
		ResolvedMetadata: map[string]any{
			"role":          "backend-feature",
			"allowed_paths": []any{"backend/**"},
		},
	})
	if identity.Scope != "backend-feature" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestFromWorkerUsesTruthfulFallbacks(t *testing.T) {
	identity := FromWorker(state.Worker{Engine: "agy"})
	if identity.Scope != "unscoped" || identity.Engine != "agy" || identity.Model != "default" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestFromWorkerPrefersRuntimeReportedModel(t *testing.T) {
	identity := FromWorker(state.Worker{
		Engine:           "codex",
		ResolvedMetadata: map[string]any{"engine": map[string]any{"model": "configured-model"}},
		EngineResult:     &state.EngineResult{Diagnostics: map[string]any{"model": "actual-model"}},
	})
	if identity.Model != "actual-model" {
		t.Fatalf("identity = %#v", identity)
	}
}
