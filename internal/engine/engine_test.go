package engine

import (
	"strings"
	"testing"

	"promptgrinder/internal/execution"
)

type testEngine struct {
	name string
}

func (e testEngine) Name() string {
	return e.name
}

func (e testEngine) Describe() Descriptor {
	return Descriptor{Name: e.name, Description: "test engine"}
}

func (e testEngine) Validate(ctx execution.Context) error {
	return nil
}

func (e testEngine) Build(ctx execution.Context, prompt []byte, executablePath string) (execution.Request, error) {
	return execution.Request{}, nil
}

func TestRegistryLookupAndList(t *testing.T) {
	registry := NewRegistry(testEngine{name: "zeta"}, testEngine{name: "alpha"})

	adapter, err := registry.Lookup("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "alpha" {
		t.Fatalf("adapter = %s, want alpha", adapter.Name())
	}
	items := registry.List()
	if len(items) != 2 || items[0].Name != "alpha" || items[1].Name != "zeta" {
		t.Fatalf("items = %#v", items)
	}
}

func TestRegistryUnknownEngine(t *testing.T) {
	registry := NewRegistry(testEngine{name: "codex"})

	_, err := registry.Lookup("missing")
	if err == nil || !strings.Contains(err.Error(), `invalid engine "missing": unknown engine`) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistryDefaultEngineLookup(t *testing.T) {
	registry := NewRegistry(testEngine{name: "codex"})

	adapter, err := registry.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "codex" {
		t.Fatalf("adapter = %s, want codex", adapter.Name())
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	registry := NewRegistry(testEngine{name: "codex"})

	err := registry.Register(testEngine{name: "codex"})
	if err == nil || !strings.Contains(err.Error(), `duplicate engine "codex"`) {
		t.Fatalf("err = %v", err)
	}

	adapter, err := registry.Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != "codex" {
		t.Fatalf("adapter = %s, want codex", adapter.Name())
	}
}

func TestRegistryRejectsUnnamedRegistration(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(testEngine{})
	if err == nil || !strings.Contains(err.Error(), "engine name is required") {
		t.Fatalf("err = %v", err)
	}
}
