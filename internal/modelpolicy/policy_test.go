package modelpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSelectsLowestCostModelThatMeetsRequirements(t *testing.T) {
	policy := Policy{Version: 1, Models: []Model{
		{ID: "high-code", Cost: "high", Capabilities: []string{"text", "code", "image"}},
		{ID: "low-text", Cost: "low", Capabilities: []string{"text"}},
		{ID: "medium-code", Cost: "medium", Capabilities: []string{"text", "code"}},
	}}
	selection, err := Resolve(policy, true, Requirements{MaxCost: "high", Capabilities: []string{"code"}})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Model != "medium-code" || selection.Cost != "medium" {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestResolveRejectsExplicitModelOutsidePolicyOrCapabilities(t *testing.T) {
	policy := Policy{Version: 1, Models: []Model{{ID: "low", Cost: "low", Capabilities: []string{"text"}}}}
	for _, requirements := range []Requirements{{Model: "missing"}, {Model: "low", MaxCost: "medium", Capabilities: []string{"code"}}} {
		_, err := Resolve(policy, true, requirements)
		if err == nil {
			t.Fatalf("Resolve(%#v) succeeded", requirements)
		}
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".promptgrinder", "models.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: 1\nmodels:\n  - id: cheap\n    cost: low\n    capabilities: [text]\n    unknown: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("err = %v", err)
	}
}
