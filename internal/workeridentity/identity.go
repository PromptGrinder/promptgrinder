package workeridentity

import (
	"strings"

	"promptgrinder/internal/state"
)

// Identity is the compact, non-secret execution identity exposed by progress
// output. Exact policy and runtime configuration remain available in worker
// state and JSON output.
type Identity struct {
	Scope  string
	Engine string
	Model  string
}

func FromWorker(worker state.Worker) Identity {
	identity := Identity{Scope: "unscoped", Engine: strings.TrimSpace(worker.Engine), Model: "default"}
	if identity.Engine == "" {
		identity.Engine = "unknown-engine"
	}
	if role, ok := stringValue(worker.ResolvedMetadata["role"]); ok {
		identity.Scope = role
	} else if role, ok := stringValue(worker.Metadata["role"]); ok {
		identity.Scope = role
	}
	if identity.Scope == "unscoped" && (hasPathPolicy(worker.ResolvedMetadata) || hasPathPolicy(worker.Metadata)) {
		identity.Scope = "slice-policy"
	}
	if engine, ok := mapValue(worker.ResolvedMetadata["engine"]); ok {
		if model, ok := engine["model"].(string); ok && strings.TrimSpace(model) != "" {
			identity.Model = strings.TrimSpace(model)
		}
	}
	if worker.EngineResult != nil {
		if model, ok := stringValue(worker.EngineResult.Diagnostics["model"]); ok {
			identity.Model = model
		}
	}
	return identity
}

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

func hasPathPolicy(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	return nonemptyList(metadata["allowed_paths"]) || nonemptyList(metadata["forbidden_paths"])
}

func nonemptyList(value any) bool {
	switch values := value.(type) {
	case []any:
		return len(values) > 0
	case []string:
		return len(values) > 0
	default:
		return false
	}
}

func mapValue(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}
