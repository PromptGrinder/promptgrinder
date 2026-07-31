package roleenhance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type RoleMergeService struct{}

type pendingWrite struct {
	rel, target string
	content     []byte
	mode        os.FileMode
	temp        string
}

func (RoleMergeService) Apply(root string, reviewed CurrentState, plan ReviewPlan, selection ApprovalSelection) (MergeResult, error) {
	stable, err := StableReviewPlan(plan.Items)
	if err != nil {
		return MergeResult{}, fmt.Errorf("invalid review plan: %w", err)
	}
	if !reflect.DeepEqual(stable.Items, plan.Items) {
		return MergeResult{}, fmt.Errorf("invalid review plan: recommendations are not in stable order")
	}
	approved, rejected, err := approvedIDs(plan, selection)
	if err != nil {
		return MergeResult{}, err
	}
	result := MergeResult{Rejected: rejected}
	if len(approved) == 0 {
		return result, nil
	}
	latest, err := LoadCurrentState(root)
	if err != nil {
		return result, fmt.Errorf("re-read role files: %w", err)
	}
	oldRoles, newRoles := roleIndex(reviewed), roleIndex(latest)
	if !bytes.Equal(reviewed.Project.Raw, latest.Project.Raw) {
		return result, fmt.Errorf("role enhancement has stale project configuration")
	}
	for id := range approved {
		item := findReviewItem(plan, id)
		oldRole, ok := oldRoles[item.Recommendation.RoleID]
		if !ok {
			return result, fmt.Errorf("reviewed role %q is missing", item.Recommendation.RoleID)
		}
		newRole, ok := newRoles[item.Recommendation.RoleID]
		if !ok || !bytes.Equal(oldRole.Raw, newRole.Raw) {
			result.Conflicts = append(result.Conflicts, MergeConflict{RecommendationID: id, Reason: "role file changed since review"})
		}
	}
	if len(result.Conflicts) > 0 {
		sort.Slice(result.Conflicts, func(i, j int) bool {
			return result.Conflicts[i].RecommendationID < result.Conflicts[j].RecommendationID
		})
		return result, fmt.Errorf("role enhancement has stale-file conflicts")
	}

	byRole := map[string][]ReviewItem{}
	for _, item := range plan.Items {
		if approved[item.Recommendation.ID] {
			byRole[item.Recommendation.RoleID] = append(byRole[item.Recommendation.RoleID], item)
		}
	}
	abs, _ := filepath.Abs(root)
	writes := make([]pendingWrite, 0, len(byRole))
	for roleID, items := range byRole {
		role := newRoles[roleID]
		var document yaml.Node
		if err := yaml.Unmarshal(role.Raw, &document); err != nil {
			return result, fmt.Errorf("parse %s: %w", role.SourcePath, err)
		}
		for _, item := range items {
			oldValue := fieldValue(role, item.Recommendation.Field)
			newValue, err := proposedValue(oldValue, item.Recommendation)
			if err != nil {
				return result, err
			}
			if !reflect.DeepEqual(oldValue, item.OldValue) {
				return result, fmt.Errorf("recommendation %q old value no longer matches", item.Recommendation.ID)
			}
			if err := setYAMLField(&document, item.Recommendation.Field, newValue); err != nil {
				return result, fmt.Errorf("recommendation %q: %w", item.Recommendation.ID, err)
			}
			setRoleField(&role, item.Recommendation.Field, newValue)
			result.Applied = append(result.Applied, MergeChange{RecommendationID: item.Recommendation.ID, RoleID: roleID, Field: item.Recommendation.Field, OldValue: oldValue, NewValue: newValue})
		}
		var out bytes.Buffer
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(document.Content[0]); err != nil {
			return result, fmt.Errorf("marshal %s: %w", role.SourcePath, err)
		}
		if err := enc.Close(); err != nil {
			return result, err
		}
		target := filepath.Join(abs, filepath.FromSlash(role.SourcePath))
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return result, fmt.Errorf("inspect %s: invalid role file", role.SourcePath)
		}
		writes = append(writes, pendingWrite{rel: role.SourcePath, target: target, content: out.Bytes(), mode: info.Mode().Perm()})
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].rel < writes[j].rel })
	for i := range writes {
		f, err := os.CreateTemp(filepath.Dir(writes[i].target), ".promptgrinder-enhance-*")
		if err != nil {
			cleanupTemps(writes)
			return result, fmt.Errorf("stage %s: %w", writes[i].rel, err)
		}
		writes[i].temp = f.Name()
		if err = f.Chmod(writes[i].mode); err == nil {
			_, err = f.Write(writes[i].content)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			cleanupTemps(writes)
			return result, fmt.Errorf("stage %s: %w", writes[i].rel, err)
		}
	}
	// Close the staging-time race before replacing any target. A changed source
	// aborts the complete batch while every staged file is still disposable.
	for _, write := range writes {
		roleID := strings.TrimSuffix(filepath.Base(write.rel), filepath.Ext(write.rel))
		currentBytes, readErr := readRegular(write.target)
		if readErr != nil || !bytes.Equal(currentBytes, newRoles[roleID].Raw) {
			cleanupTemps(writes)
			return result, fmt.Errorf("role enhancement source changed while staging: %s", write.rel)
		}
	}
	for _, write := range writes {
		if err := os.Rename(write.temp, write.target); err != nil {
			cleanupTemps(writes)
			return result, fmt.Errorf("replace %s: %w", write.rel, err)
		}
		result.Files = append(result.Files, write.rel)
	}
	return result, nil
}

func approvedIDs(plan ReviewPlan, selection ApprovalSelection) (map[string]bool, []string, error) {
	all := map[string]ReviewItem{}
	for _, item := range plan.Items {
		all[item.Recommendation.ID] = item
	}
	approved := map[string]bool{}
	switch selection.Mode {
	case ApprovalRejectAll:
		if len(selection.RecommendationIDs) > 0 {
			return nil, nil, fmt.Errorf("reject-all cannot include recommendation IDs")
		}
	case ApprovalApplyAll:
		if len(selection.RecommendationIDs) > 0 {
			return nil, nil, fmt.Errorf("apply-all cannot include recommendation IDs")
		}
		for id, item := range all {
			if item.Recommendation.Operation == OperationRemove {
				return nil, nil, fmt.Errorf("removal %q must be individually selected", id)
			}
			approved[id] = true
		}
	case ApprovalApplySelected:
		if len(selection.RecommendationIDs) == 0 {
			return nil, nil, fmt.Errorf("apply-selected requires at least one recommendation ID")
		}
		for _, id := range selection.RecommendationIDs {
			if _, ok := all[id]; !ok {
				return nil, nil, fmt.Errorf("unknown recommendation ID %q", id)
			}
			if approved[id] {
				return nil, nil, fmt.Errorf("duplicate recommendation ID %q", id)
			}
			approved[id] = true
		}
	default:
		return nil, nil, fmt.Errorf("invalid approval mode %q", selection.Mode)
	}
	var rejected []string
	for id := range all {
		if !approved[id] {
			rejected = append(rejected, id)
		}
	}
	sort.Strings(rejected)
	return approved, rejected, nil
}
func findReviewItem(plan ReviewPlan, id string) ReviewItem {
	for _, i := range plan.Items {
		if i.Recommendation.ID == id {
			return i
		}
	}
	return ReviewItem{}
}
func proposedValue(old any, r Recommendation) (any, error) {
	vals, ok := stringValues(r.Value)
	if !ok || len(vals) == 0 {
		return nil, fmt.Errorf("recommendation %q has invalid value", r.ID)
	}
	if r.Operation == OperationSet {
		if _, ok := old.(string); ok {
			if len(vals) != 1 {
				return nil, fmt.Errorf("recommendation %q requires one string value", r.ID)
			}
			return vals[0], nil
		}
		return vals, nil
	}
	if len(vals) != 1 {
		return nil, fmt.Errorf("recommendation %q requires one string value", r.ID)
	}
	current, ok := old.([]string)
	if !ok {
		return nil, fmt.Errorf("recommendation %q requires a list field", r.ID)
	}
	out := append([]string(nil), current...)
	value := vals[0]
	if r.Operation == OperationAppend {
		for _, v := range out {
			if v == value {
				return out, nil
			}
		}
		return append(out, value), nil
	}
	if r.Operation == OperationRemove {
		for i, v := range out {
			if v == value {
				return append(out[:i:i], out[i+1:]...), nil
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported operation %q", r.Operation)
}
func setRoleField(r *RoleSnapshot, field string, v any) {
	vals, _ := stringValues(v)
	switch field {
	case "name":
		r.Name = vals[0]
	case "description":
		r.Description = vals[0]
	case "technology":
		r.Technology = vals
	case "allowed_paths":
		r.AllowedPaths = vals
	case "runtime.preferred":
		r.Runtime.Preferred = vals[0]
	case "quality_gates":
		r.QualityGates = vals
	}
}
func setYAMLField(doc *yaml.Node, field string, value any) error {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("role YAML must be a mapping")
	}
	parts := strings.Split(field, ".")
	node := doc.Content[0]
	for i, key := range parts {
		var target *yaml.Node
		for j := 0; j < len(node.Content); j += 2 {
			if node.Content[j].Value == key {
				target = node.Content[j+1]
				break
			}
		}
		if i == len(parts)-1 {
			var replacement yaml.Node
			if err := replacement.Encode(value); err != nil {
				return err
			}
			if target != nil {
				*target = replacement
			} else {
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &replacement)
			}
			return nil
		}
		if target == nil {
			target = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, target)
		}
		if target.Kind != yaml.MappingNode {
			return fmt.Errorf("field %q is not a mapping", key)
		}
		node = target
	}
	return nil
}
func cleanupTemps(w []pendingWrite) {
	for _, x := range w {
		if x.temp != "" {
			_ = os.Remove(x.temp)
		}
	}
}
