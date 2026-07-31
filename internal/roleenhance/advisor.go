package roleenhance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"promptgrinder/internal/workerlaunch"
)

const AdvisorSchemaVersion = "promptgrinder.role-advisor/v1"

// AdvisorRuntime is the replaceable, runtime-neutral execution boundary. The
// runtime receives immutable JSON and returns JSON; it is deliberately not
// given a repository root, filesystem, or writer.
type AdvisorRuntime interface {
	workerlaunch.CapabilityProvider
	Advise(context.Context, []byte) ([]byte, error)
}

type AdvisorRequest struct {
	SchemaVersion string       `json:"schema_version"`
	Current       CurrentState `json:"current"`
	Evidence      Evidence     `json:"evidence"`
}

type AdvisorResponse struct {
	SchemaVersion   string           `json:"schema_version"`
	Recommendations []Recommendation `json:"recommendations"`
}

type AiRoleAdvisor struct {
	Runtime AdvisorRuntime
}

// AdvisorRuntimeRegistry keeps production selection symbolic and independent
// of any particular AI provider.
type AdvisorRuntimeRegistry struct {
	runtimes map[string]AdvisorRuntime
}

func (r *AdvisorRuntimeRegistry) Register(name string, runtime AdvisorRuntime) error {
	name = strings.TrimSpace(name)
	if name == "" || runtime == nil {
		return fmt.Errorf("advisor runtime name and implementation are required")
	}
	if r.runtimes == nil {
		r.runtimes = map[string]AdvisorRuntime{}
	}
	if _, exists := r.runtimes[name]; exists {
		return fmt.Errorf("advisor runtime %q is already registered", name)
	}
	r.runtimes[name] = runtime
	return nil
}

func (r AdvisorRuntimeRegistry) Advisor(name string) (AiRoleAdvisor, error) {
	runtime, ok := r.runtimes[name]
	if !ok {
		return AiRoleAdvisor{}, fmt.Errorf("advisor runtime %q is not registered", name)
	}
	return AiRoleAdvisor{Runtime: runtime}, nil
}

func (a AiRoleAdvisor) Recommend(ctx context.Context, current CurrentState, evidence Evidence) (ReviewPlan, error) {
	if a.Runtime == nil {
		return ReviewPlan{}, fmt.Errorf("role advisor runtime is not configured")
	}
	required := workerlaunch.Capabilities{Headless: true, StructuredOutput: true, Sandbox: true}
	if err := workerlaunch.Negotiate(advisorLauncher{a.Runtime}, required); err != nil {
		return ReviewPlan{}, fmt.Errorf("role advisor capability negotiation: %w", err)
	}
	request, err := BuildAdvisorRequest(current, evidence)
	if err != nil {
		return ReviewPlan{}, err
	}
	raw, err := a.Runtime.Advise(ctx, request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ReviewPlan{}, fmt.Errorf("role advisor timed out: %w", context.DeadlineExceeded)
		}
		return ReviewPlan{}, fmt.Errorf("role advisor runtime failed: %w", err)
	}
	response, err := parseAdvisorResponse(raw)
	if err != nil {
		return ReviewPlan{}, err
	}
	if err := ValidateRecommendations(current, evidence, response.Recommendations); err != nil {
		return ReviewPlan{}, err
	}
	return (RoleDiffGenerator{}).Generate(current, response.Recommendations)
}

// advisorLauncher lets the established runtime capability negotiation remain
// the single authority without pretending an advisor is a named worker.
type advisorLauncher struct{ AdvisorRuntime }

func (advisorLauncher) Launch(context.Context, workerlaunch.LaunchRequest) (workerlaunch.LaunchResult, error) {
	return workerlaunch.LaunchResult{}, fmt.Errorf("advisor runtime cannot launch workers")
}

func BuildAdvisorRequest(current CurrentState, evidence Evidence) ([]byte, error) {
	current = normalizedCurrent(current)
	evidence = normalizedEvidence(evidence)
	if len(current.Roles) > 256 {
		return nil, fmt.Errorf("role advisor request exceeds role limit (256)")
	}
	if len(evidence.Sources) > defaultMaxFiles || len(evidence.Facts) > defaultMaxFiles {
		return nil, fmt.Errorf("role advisor request exceeds evidence item limit (%d)", defaultMaxFiles)
	}
	total := 0
	for _, source := range evidence.Sources {
		if len(source.Excerpt) > defaultMaxFileBytes {
			return nil, fmt.Errorf("role advisor evidence %q exceeds per-source limit", source.Path)
		}
		total += len(source.Excerpt)
	}
	if total > defaultMaxTotalBytes {
		return nil, fmt.Errorf("role advisor request exceeds evidence byte limit (%d)", defaultMaxTotalBytes)
	}
	return json.Marshal(AdvisorRequest{SchemaVersion: AdvisorSchemaVersion, Current: current, Evidence: evidence})
}

func parseAdvisorResponse(raw []byte) (AdvisorResponse, error) {
	var response AdvisorResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return AdvisorResponse{}, fmt.Errorf("invalid role advisor JSON/schema: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AdvisorResponse{}, fmt.Errorf("invalid role advisor JSON/schema: %w", err)
	}
	if response.SchemaVersion != AdvisorSchemaVersion {
		return AdvisorResponse{}, fmt.Errorf("invalid role advisor schema version %q", response.SchemaVersion)
	}
	if response.Recommendations == nil {
		return AdvisorResponse{}, fmt.Errorf("invalid role advisor JSON/schema: recommendations is required")
	}
	return response, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

var secretLooking = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|password|passwd|private[_-]?key|secret)\s*[:=]\s*\S+|-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:sk|ghp|github_pat)_[A-Za-z0-9_-]{12,}`)

var supportedOperations = map[string]map[Operation]bool{
	"name":              {OperationSet: true},
	"description":       {OperationSet: true},
	"technology":        {OperationSet: true, OperationAppend: true, OperationRemove: true},
	"allowed_paths":     {OperationSet: true, OperationAppend: true, OperationRemove: true},
	"runtime.preferred": {OperationSet: true},
	"quality_gates":     {OperationSet: true, OperationAppend: true, OperationRemove: true},
}

func ValidateRecommendations(current CurrentState, evidence Evidence, recommendations []Recommendation) error {
	roles := roleIndex(current)
	for _, recommendation := range recommendations {
		if _, ok := roles[recommendation.RoleID]; !ok {
			return fmt.Errorf("recommendation %q references unknown role %q", recommendation.ID, recommendation.RoleID)
		}
		operations, ok := supportedOperations[recommendation.Field]
		if !ok || !operations[recommendation.Operation] {
			return fmt.Errorf("recommendation %q has unsupported operation %q for field %q", recommendation.ID, recommendation.Operation, recommendation.Field)
		}
		if strings.TrimSpace(recommendation.Explanation) == "" {
			return fmt.Errorf("recommendation %q requires a nonempty explanation", recommendation.ID)
		}
		if recommendation.Confidence != ConfidenceLow && recommendation.Confidence != ConfidenceMedium && recommendation.Confidence != ConfidenceHigh {
			return fmt.Errorf("recommendation %q has invalid confidence %q", recommendation.ID, recommendation.Confidence)
		}
		encoded, err := json.Marshal(recommendation)
		if err != nil || secretLooking.Match(encoded) {
			return fmt.Errorf("recommendation %q contains secret-looking content", recommendation.ID)
		}
		if err := validateGroundedValue(recommendation, current, evidence); err != nil {
			return err
		}
	}
	if err := ValidateRecommendationEvidence(recommendations, evidence); err != nil {
		return err
	}
	items := make([]ReviewItem, len(recommendations))
	for i, r := range recommendations {
		items[i].Recommendation = r
	}
	_, err := StableReviewPlan(items)
	return err
}

func validateGroundedValue(r Recommendation, current CurrentState, evidence Evidence) error {
	values, ok := stringValues(r.Value)
	if !ok || len(values) == 0 {
		return fmt.Errorf("recommendation %q has invalid value for field %q", r.ID, r.Field)
	}
	if r.Operation == OperationRemove && len(values) != 1 {
		return fmt.Errorf("recommendation %q removal requires one string value", r.ID)
	}
	corpus := groundingCorpus(current, evidence)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("recommendation %q contains an empty value", r.ID)
		}
		switch r.Field {
		case "technology":
			if !knownTechnology(current, evidence, value) {
				return fmt.Errorf("recommendation %q proposes ungrounded technology %q", r.ID, value)
			}
		case "quality_gates", "runtime.preferred":
			if !strings.Contains(corpus, strings.ToLower(value)) {
				return fmt.Errorf("recommendation %q proposes ungrounded %s %q", r.ID, r.Field, value)
			}
		case "allowed_paths":
			clean := filepath.ToSlash(filepath.Clean(value))
			if clean == "." || filepath.IsAbs(value) || clean == ".." || strings.HasPrefix(clean, "../") || !evidenceHasPath(evidence, clean) {
				return fmt.Errorf("recommendation %q proposes ungrounded context path %q", r.ID, value)
			}
		}
	}
	return nil
}

func knownTechnology(current CurrentState, evidence Evidence, proposed string) bool {
	for _, value := range append(append([]string{}, current.Project.Languages...), current.Project.Frameworks...) {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(proposed)) {
			return true
		}
	}
	for _, role := range current.Roles {
		for _, value := range role.Technology {
			if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(proposed)) {
				return true
			}
		}
	}
	if strings.EqualFold(strings.TrimSpace(proposed), "Go") {
		for _, source := range evidence.Sources {
			if source.Kind == EvidenceRepository && source.Path == "go.mod" {
				return true
			}
		}
		for _, fact := range evidence.Facts {
			if fact.Kind == EvidenceRepository && fact.Path == "go.mod" {
				return true
			}
		}
	}
	return false
}

func stringValues(value any) ([]string, bool) {
	switch v := value.(type) {
	case string:
		return []string{v}, true
	case []any:
		out := make([]string, len(v))
		for i := range v {
			var ok bool
			out[i], ok = v[i].(string)
			if !ok {
				return nil, false
			}
		}
		return out, true
	case []string:
		return append([]string(nil), v...), true
	default:
		return nil, false
	}
}

func evidenceHasPath(e Evidence, proposed string) bool {
	for _, s := range e.Sources {
		if s.Path == proposed || strings.HasPrefix(s.Path, strings.TrimSuffix(proposed, "/")+"/") {
			return true
		}
	}
	for _, f := range e.Facts {
		if f.Path == proposed || f.Value == proposed || strings.HasPrefix(f.Path, strings.TrimSuffix(proposed, "/")+"/") {
			return true
		}
	}
	return false
}

func groundingCorpus(current CurrentState, evidence Evidence) string {
	var values []string
	values = append(values, current.Project.Name, current.Project.GeneratedBy)
	values = append(values, current.Project.Languages...)
	values = append(values, current.Project.Frameworks...)
	values = append(values, current.Project.Roles...)
	for _, role := range current.Roles {
		values = append(values, role.ID, role.Name, role.Description, role.Runtime.Preferred)
		values = append(values, role.Technology...)
		values = append(values, role.AllowedPaths...)
		values = append(values, role.QualityGates...)
	}
	for _, source := range evidence.Sources {
		values = append(values, string(source.Kind), source.Path, source.Excerpt)
	}
	for _, fact := range evidence.Facts {
		values = append(values, string(fact.Kind), fact.Path, fact.Key, fact.Value, fact.Key+"="+fact.Value)
	}
	return strings.ToLower(strings.Join(values, "\n"))
}

func roleIndex(current CurrentState) map[string]RoleSnapshot {
	out := make(map[string]RoleSnapshot, len(current.Roles))
	for _, role := range current.Roles {
		out[role.ID] = role
	}
	return out
}

func fieldValue(role RoleSnapshot, field string) any {
	switch field {
	case "name":
		return role.Name
	case "description":
		return role.Description
	case "technology":
		return append([]string(nil), role.Technology...)
	case "allowed_paths":
		return append([]string(nil), role.AllowedPaths...)
	case "runtime.preferred":
		return role.Runtime.Preferred
	case "quality_gates":
		return append([]string(nil), role.QualityGates...)
	default:
		return nil
	}
}

func normalizedCurrent(in CurrentState) CurrentState {
	out := in
	out.Project.Raw = nil
	out.Project.Languages = sortedStrings(out.Project.Languages)
	out.Project.Frameworks = sortedStrings(out.Project.Frameworks)
	out.Project.Roles = sortedStrings(out.Project.Roles)
	out.Roles = append([]RoleSnapshot(nil), in.Roles...)
	for i := range out.Roles {
		out.Roles[i].Raw = nil
		out.Roles[i].Technology = sortedStrings(out.Roles[i].Technology)
		out.Roles[i].AllowedPaths = sortedStrings(out.Roles[i].AllowedPaths)
		out.Roles[i].QualityGates = sortedStrings(out.Roles[i].QualityGates)
	}
	sort.Slice(out.Roles, func(i, j int) bool { return out.Roles[i].ID < out.Roles[j].ID })
	return out
}

func normalizedEvidence(in Evidence) Evidence {
	out := in
	out.Sources = append([]EvidenceSource(nil), in.Sources...)
	out.Facts = append([]EvidenceFact(nil), in.Facts...)
	sort.Slice(out.Sources, func(i, j int) bool {
		a, b := out.Sources[i], out.Sources[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Kind < b.Kind
	})
	sort.Slice(out.Facts, func(i, j int) bool {
		a, b := out.Facts[i], out.Facts[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Value < b.Value
	})
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
