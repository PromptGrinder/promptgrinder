// Package modelpolicy loads repository-owned model cost and capability policy.
package modelpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const Path = ".promptgrinder/models.yaml"

// Policy is intentionally repository-owned. The runtime remains the authority
// on whether a configured model is currently selectable by the signed-in user.
type Policy struct {
	Version int     `yaml:"version"`
	Default string  `yaml:"default"`
	Models  []Model `yaml:"models"`
}

type Model struct {
	ID           string   `yaml:"id"`
	Cost         string   `yaml:"cost"`
	Capabilities []string `yaml:"capabilities"`
}

// Requirements are resolved before an engine is launched. Cost is an
// upper-bound: low permits only low-cost configured models.
type Requirements struct {
	Model        string
	MaxCost      string
	Capabilities []string
}

// Selection contains the model string passed to the engine and its local
// policy annotations. Empty Model preserves the engine's configured default.
type Selection struct {
	Model        string
	Cost         string
	Capabilities []string
}

func Load(root string) (Policy, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(Path))
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, fmt.Errorf("read model policy %s: %w", path, err)
	}
	var policy Policy
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, true, fmt.Errorf("parse model policy %s: %w", path, err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, true, fmt.Errorf("model policy %s: %w", path, err)
	}
	return policy, true, nil
}

func (p Policy) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("version %d is unsupported; want 1", p.Version)
	}
	if len(p.Models) == 0 {
		return fmt.Errorf("models must contain at least one configured model")
	}
	seen := map[string]bool{}
	for _, model := range p.Models {
		if strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("model id must be a non-empty string")
		}
		if seen[model.ID] {
			return fmt.Errorf("models contains duplicate id %q", model.ID)
		}
		seen[model.ID] = true
		if costRank(model.Cost) < 0 {
			return fmt.Errorf("model %q cost must be low, medium, or high", model.ID)
		}
		if err := validateCapabilities(model.Capabilities); err != nil {
			return fmt.Errorf("model %q: %w", model.ID, err)
		}
	}
	if p.Default != "" && p.model(p.Default) == nil {
		return fmt.Errorf("default model %q is not declared in models", p.Default)
	}
	return nil
}

func Resolve(policy Policy, present bool, requirements Requirements) (Selection, error) {
	requirements.Model = strings.TrimSpace(requirements.Model)
	requirements.MaxCost = strings.TrimSpace(requirements.MaxCost)
	if requirements.MaxCost != "" && costRank(requirements.MaxCost) < 0 {
		return Selection{}, fmt.Errorf("engine.max_cost must be low, medium, or high")
	}
	if err := validateCapabilities(requirements.Capabilities); err != nil {
		return Selection{}, fmt.Errorf("engine.capabilities: %w", err)
	}
	if !present {
		if requirements.MaxCost != "" || len(requirements.Capabilities) > 0 {
			return Selection{}, fmt.Errorf("engine.max_cost and engine.capabilities require %s", Path)
		}
		return Selection{Model: requirements.Model}, nil
	}

	if requirements.Model != "" {
		model := policy.model(requirements.Model)
		if model == nil {
			return Selection{}, fmt.Errorf("model %q is not allowed by %s; allowed models: %s", requirements.Model, Path, strings.Join(policy.ids(), ", "))
		}
		if err := satisfies(*model, requirements); err != nil {
			return Selection{}, err
		}
		return Selection{Model: model.ID, Cost: model.Cost, Capabilities: append([]string(nil), model.Capabilities...)}, nil
	}

	if requirements.MaxCost == "" && len(requirements.Capabilities) == 0 && policy.Default != "" {
		model := policy.model(policy.Default)
		return Selection{Model: model.ID, Cost: model.Cost, Capabilities: append([]string(nil), model.Capabilities...)}, nil
	}
	models := append([]Model(nil), policy.Models...)
	sort.Slice(models, func(i, j int) bool {
		left, right := costRank(models[i].Cost), costRank(models[j].Cost)
		return left < right || left == right && models[i].ID < models[j].ID
	})
	for _, model := range models {
		if satisfies(model, requirements) == nil {
			return Selection{Model: model.ID, Cost: model.Cost, Capabilities: append([]string(nil), model.Capabilities...)}, nil
		}
	}
	return Selection{}, fmt.Errorf("no model in %s satisfies max_cost %q and capabilities %s", Path, requirements.MaxCost, strings.Join(requirements.Capabilities, ", "))
}

func (p Policy) model(id string) *Model {
	for i := range p.Models {
		if p.Models[i].ID == id {
			return &p.Models[i]
		}
	}
	return nil
}

func (p Policy) ids() []string {
	ids := make([]string, 0, len(p.Models))
	for _, model := range p.Models {
		ids = append(ids, model.ID)
	}
	sort.Strings(ids)
	return ids
}

func satisfies(model Model, requirements Requirements) error {
	if requirements.MaxCost != "" && costRank(model.Cost) > costRank(requirements.MaxCost) {
		return fmt.Errorf("model %q has cost tier %q, above requested maximum %q", model.ID, model.Cost, requirements.MaxCost)
	}
	available := map[string]bool{}
	for _, capability := range model.Capabilities {
		available[capability] = true
	}
	for _, required := range requirements.Capabilities {
		if !available[required] {
			return fmt.Errorf("model %q does not provide required capability %q", model.ID, required)
		}
	}
	return nil
}

func costRank(value string) int {
	switch value {
	case "low":
		return 0
	case "medium":
		return 1
	case "high":
		return 2
	default:
		return -1
	}
}

func validateCapabilities(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "text" && value != "image" && value != "code" && value != "web-search" {
			return fmt.Errorf("capabilities may contain only text, image, code, or web-search")
		}
		if seen[value] {
			return fmt.Errorf("capabilities contains duplicate %q", value)
		}
		seen[value] = true
	}
	return nil
}
