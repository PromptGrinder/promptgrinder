package engine

import (
	"fmt"
	"sort"

	"promptgrinder/internal/execution"
	"promptgrinder/internal/state"
)

type Capabilities struct {
	SupportsModel               bool `json:"supports_model"`
	SupportsProfile             bool `json:"supports_profile"`
	SupportsSandbox             bool `json:"supports_sandbox"`
	SupportsApproval            bool `json:"supports_approval"`
	SupportsWorkingDirectory    bool `json:"supports_working_directory"`
	SupportsWebSearch           bool `json:"supports_web_search"`
	SupportsImages              bool `json:"supports_images"`
	SupportsResume              bool `json:"supports_resume"`
	SupportsStructuredOutput    bool `json:"supports_structured_output"`
	SupportsTokenUsage          bool `json:"supports_token_usage"`
	SupportsCostUsage           bool `json:"supports_cost_usage"`
	SupportsHeadless            bool `json:"supports_headless"`
	SupportsInteractiveTerminal bool `json:"supports_interactive_terminal"`
	SupportsEnv                 bool `json:"supports_env"`
}

type Descriptor struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	Capabilities Capabilities `json:"capabilities"`
}

type Engine interface {
	Name() string
	Describe() Descriptor
	Validate(ctx execution.Context) error
	Build(ctx execution.Context, prompt []byte, executablePath string) (execution.Request, error)
}

type MetadataResolver interface {
	ResolveMetadata(ctx execution.Context) (map[string]any, error)
}

type ResultParser interface {
	ParseResult(ctx execution.Context, log []byte) state.EngineResult
}

type Registry struct {
	engines map[string]Engine
}

func NewRegistry(engines ...Engine) Registry {
	registry := Registry{engines: map[string]Engine{}}
	for _, adapter := range engines {
		_ = registry.Register(adapter)
	}
	return registry
}

func (r *Registry) Register(adapter Engine) error {
	if adapter == nil {
		return fmt.Errorf("invalid engine registration: adapter is nil")
	}
	if adapter.Name() == "" {
		return fmt.Errorf("invalid engine registration: engine name is required")
	}
	if r.engines == nil {
		r.engines = map[string]Engine{}
	}
	if _, exists := r.engines[adapter.Name()]; exists {
		return fmt.Errorf("invalid engine registration: duplicate engine %q", adapter.Name())
	}
	r.engines[adapter.Name()] = adapter
	return nil
}

func (r Registry) Lookup(name string) (Engine, error) {
	if name == "" {
		name = "codex"
	}
	adapter, ok := r.engines[name]
	if !ok {
		return nil, fmt.Errorf("invalid engine %q: unknown engine", name)
	}
	return adapter, nil
}

func (r Registry) List() []Descriptor {
	out := []Descriptor{}
	for _, adapter := range r.engines {
		out = append(out, adapter.Describe())
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}
