package engine_test

import (
	"testing"

	"promptgrinder/internal/engine"
	"promptgrinder/internal/engine/codex"
)

func TestBuiltInCodexEngineCanBeResolved(t *testing.T) {
	registry := engine.NewRegistry(codex.Engine{})

	adapter, err := registry.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "codex" {
		t.Fatalf("adapter = %s, want codex", adapter.Name())
	}

	descriptor := adapter.Describe()
	if descriptor.Name != "codex" || descriptor.Description == "" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	caps := descriptor.Capabilities
	if !caps.SupportsModel ||
		!caps.SupportsProfile ||
		!caps.SupportsSandbox ||
		!caps.SupportsApproval ||
		!caps.SupportsWorkingDirectory ||
		!caps.SupportsWebSearch ||
		!caps.SupportsImages ||
		!caps.SupportsHeadless ||
		!caps.SupportsInteractiveTerminal {
		t.Fatalf("codex capabilities = %#v", caps)
	}
	if caps.SupportsTokenUsage || caps.SupportsCostUsage {
		t.Fatalf("codex should not claim token or cost support: %#v", caps)
	}
}
