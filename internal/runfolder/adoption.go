package runfolder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"promptgrinder/internal/markdown"
)

const sequenceStateVersion = 2

func (s sequenceStore) validateExplicitAdoption(folder, repoRoot string, prompts []Prompt, sequenceID string) (SequenceState, *SequenceAdoption, error) {
	if !matches(sequenceID, `^seq_[0-9a-f]{16}$`) {
		return SequenceState{}, nil, fmt.Errorf("invalid sequence id %q; expected seq_ followed by 16 lowercase hexadecimal characters", sequenceID)
	}
	sequence, err := s.load(sequenceID)
	if errors.Is(err, os.ErrNotExist) {
		return SequenceState{}, nil, fmt.Errorf("sequence %s was not found under %s; no run state was created or changed", sequenceID, s.Root)
	}
	if err != nil {
		return SequenceState{}, nil, fmt.Errorf("load sequence %s: %w", sequenceID, err)
	}
	if sequence.SequenceID == "" || sequence.SequenceID != sequenceID {
		return SequenceState{}, nil, fmt.Errorf("sequence state %s does not identify requested sequence %s; no run state was changed", sequence.SequenceID, sequenceID)
	}
	if sequence.StateVersion > sequenceStateVersion {
		return SequenceState{}, nil, fmt.Errorf("sequence %s uses unsupported state version %d; this PromptGrinder supports up to version %d", sequenceID, sequence.StateVersion, sequenceStateVersion)
	}
	if sequence.Status == "completed" || sequence.Status == "cancelled" {
		return SequenceState{}, nil, fmt.Errorf("sequence %s is %s and cannot be adopted; only unfinished sequences are eligible", sequenceID, sequence.Status)
	}
	if err := s.validateAdoptionLocation(sequence, folder, repoRoot); err != nil {
		return SequenceState{}, nil, err
	}
	if len(sequence.Items) != len(prompts) {
		return SequenceState{}, nil, fmt.Errorf("sequence %s has %d prompts but folder %s now has %d; explicit adoption requires the same prompt names and ordering", sequenceID, len(sequence.Items), folder, len(prompts))
	}

	retained := make([]string, 0)
	policyChanges := make([]PolicyHashChange, 0)
	restartAt := ""
	seenUnfinished := false
	for index, item := range sequence.Items {
		prompt := prompts[index]
		if item.PromptName != prompt.Name {
			return SequenceState{}, nil, fmt.Errorf("sequence %s prompt order mismatch at position %d: state has %s, folder has %s", sequenceID, index+1, item.PromptName, prompt.Name)
		}
		if err := s.validateAdoptionIdentity(sequence, item, prompt, repoRoot); err != nil {
			return SequenceState{}, nil, err
		}

		complete := item.Status == "succeeded" || item.Status == "skipped"
		if complete {
			if seenUnfinished {
				return SequenceState{}, nil, fmt.Errorf("sequence %s has completed prompt %s after an unfinished prompt; only a contiguous succeeded/skipped prefix can be adopted", sequenceID, item.PromptName)
			}
			if err := s.validateCompletedPromptContent(sequence, item, prompt, repoRoot); err != nil {
				return SequenceState{}, nil, err
			}
			retained = append(retained, item.PromptName)
		} else {
			seenUnfinished = true
			if restartAt == "" {
				restartAt = item.PromptName
			}
		}

		if sequence.StateVersion >= sequenceStateVersion {
			currentPolicyHash, hashErr := promptPolicyHash(repoRoot, prompt)
			if hashErr != nil {
				return SequenceState{}, nil, fmt.Errorf("fingerprint current role policy for %s: %w", prompt.Name, hashErr)
			}
			if item.PolicyHash != currentPolicyHash {
				policyChanges = append(policyChanges, PolicyHashChange{PromptName: prompt.Name, PreviousHash: item.PolicyHash, CurrentHash: currentPolicyHash, Retained: complete})
			}
		}
	}
	if restartAt == "" {
		return SequenceState{}, nil, fmt.Errorf("sequence %s has no failed or pending slice to restart and cannot be adopted as unfinished", sequenceID)
	}

	adoption := &SequenceAdoption{
		SequenceID:          sequenceID,
		Explicit:            true,
		MigratedLegacyState: sequence.StateVersion < sequenceStateVersion,
		RetainedPrompts:     retained,
		RestartAt:           restartAt,
		PolicyHashChanges:   policyChanges,
		AdoptedAt:           time.Now().UTC(),
	}
	return sequence, adoption, nil
}

func (s sequenceStore) validateAdoptionLocation(sequence SequenceState, folder, repoRoot string) error {
	if sequence.Folder != "" {
		if filepath.Clean(sequence.Folder) != filepath.Clean(folder) {
			return fmt.Errorf("sequence %s belongs to folder %s, not %s", sequence.SequenceID, sequence.Folder, folder)
		}
	} else if !legacyFolderEvidence(sequence, folder) {
		return legacyMigrationError(sequence.SequenceID, "folder identity is absent and prompt paths do not prove the requested folder")
	}

	if sequence.RepositoryPath != "" {
		if filepath.Clean(sequence.RepositoryPath) != filepath.Clean(repoRoot) {
			return fmt.Errorf("sequence %s belongs to repository %s, not %s", sequence.SequenceID, sequence.RepositoryPath, repoRoot)
		}
	} else if !s.legacyRepositoryEvidence(sequence, repoRoot) {
		return legacyMigrationError(sequence.SequenceID, "repository identity is absent and no checkpoint or commit belongs to the requested repository")
	}
	return nil
}

func legacyFolderEvidence(sequence SequenceState, folder string) bool {
	if len(sequence.Items) == 0 {
		return false
	}
	for _, item := range sequence.Items {
		if item.PromptPath == "" || filepath.Clean(filepath.Dir(item.PromptPath)) != filepath.Clean(folder) {
			return false
		}
	}
	return true
}

func (s sequenceStore) validateAdoptionIdentity(sequence SequenceState, item SequenceItem, prompt Prompt, repoRoot string) error {
	if sequence.StateVersion >= sequenceStateVersion {
		if item.PromptID != prompt.ID {
			return fmt.Errorf("sequence %s task identity mismatch for %s: state has %q, folder has %q", sequence.SequenceID, prompt.Name, item.PromptID, prompt.ID)
		}
		if !slices.Equal(item.DependsOn, prompt.DependsOn) {
			return fmt.Errorf("sequence %s dependency mismatch for %s: state has [%s], folder has [%s]", sequence.SequenceID, prompt.Name, strings.Join(item.DependsOn, ", "), strings.Join(prompt.DependsOn, ", "))
		}
		return nil
	}

	unchanged, err := legacyCombinedHashMatches(item, prompt, repoRoot)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	if snapshot, ok := s.legacyPromptSnapshot(sequence.SequenceID, item, prompt, repoRoot); ok {
		oldID, oldDependencies, parseErr := promptIdentityFromContent(prompt.Name, snapshot)
		if parseErr == nil && oldID == prompt.ID && slices.Equal(oldDependencies, prompt.DependsOn) {
			return nil
		}
	}
	return legacyMigrationError(sequence.SequenceID, fmt.Sprintf("dependency identity for %s is absent and current metadata cannot be matched to checkpoint evidence", prompt.Name))
}

func (s sequenceStore) validateCompletedPromptContent(sequence SequenceState, item SequenceItem, prompt Prompt, repoRoot string) error {
	currentHash, err := fileHash(prompt.Path)
	if err != nil {
		return err
	}
	if sequence.StateVersion >= sequenceStateVersion {
		if item.PromptHash == "" {
			return legacyMigrationError(sequence.SequenceID, fmt.Sprintf("completed prompt %s has no separate content fingerprint", prompt.Name))
		}
		if item.PromptHash != currentHash {
			return fmt.Errorf("sequence %s completed prompt content changed for %s; completed slices are never rerun or silently adopted", sequence.SequenceID, prompt.Name)
		}
		return nil
	}

	unchanged, err := legacyCombinedHashMatches(item, prompt, repoRoot)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	if snapshot, ok := s.legacyPromptSnapshot(sequence.SequenceID, item, prompt, repoRoot); ok && hashBytes(snapshot) == currentHash {
		return nil
	}
	return legacyMigrationError(sequence.SequenceID, fmt.Sprintf("completed prompt %s uses a legacy combined content/policy hash and no checkpoint or commit proves its prompt content is unchanged", prompt.Name))
}

func legacyCombinedHashMatches(item SequenceItem, prompt Prompt, repoRoot string) (bool, error) {
	rawHash, err := fileHash(prompt.Path)
	if err != nil {
		return false, err
	}
	if item.ContentHash == rawHash {
		return true, nil
	}
	copyPrompt := prompt
	current := []Prompt{copyPrompt}
	if err := applyRolePolicies(repoRoot, current); err != nil {
		return false, nil
	}
	effectiveHash, err := promptContentHash(current[0].Path, current[0].RolePolicy)
	return err == nil && item.ContentHash == effectiveHash, err
}

func promptIdentityFromContent(name string, data []byte) (string, []string, error) {
	task, err := markdown.Parse(string(data))
	if err != nil {
		return "", nil, err
	}
	id, _ := task.Metadata["id"].(string)
	if id == "" {
		id = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return id, stringListValue(task.Metadata["depends_on"]), nil
}

func (s sequenceStore) legacyRepositoryEvidence(sequence SequenceState, repoRoot string) bool {
	for _, item := range sequence.Items {
		promptState, err := s.loadPromptState(sequence.SequenceID, item.PromptName)
		if err != nil {
			continue
		}
		for _, revision := range promptStateRevisions(promptState) {
			if exec.Command("git", "-C", repoRoot, "cat-file", "-e", revision+"^{commit}").Run() == nil {
				return true
			}
		}
	}
	return false
}

func (s sequenceStore) legacyPromptSnapshot(sequenceID string, item SequenceItem, prompt Prompt, repoRoot string) ([]byte, bool) {
	promptState, err := s.loadPromptState(sequenceID, item.PromptName)
	if err != nil {
		return nil, false
	}
	relative, err := filepath.Rel(repoRoot, prompt.Path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, false
	}
	gitPath := filepath.ToSlash(relative)
	for _, revision := range promptStateRevisions(promptState) {
		data, showErr := exec.Command("git", "-C", repoRoot, "show", revision+":"+gitPath).Output()
		if showErr == nil {
			return data, true
		}
	}
	return nil, false
}

func (s sequenceStore) loadPromptState(sequenceID, promptName string) (PromptState, error) {
	homeDir := filepath.Dir(filepath.Dir(s.Root))
	path := filepath.Join(folderStateRoot(homeDir, sequenceID), "prompts", safeName(promptName)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptState{}, err
	}
	var promptState PromptState
	if err := json.Unmarshal(data, &promptState); err != nil {
		return PromptState{}, err
	}
	return promptState, nil
}

func promptStateRevisions(promptState PromptState) []string {
	revisions := make([]string, 0, 3)
	for _, revision := range []string{promptState.CommitSHA, promptState.GitSHAAfter, promptState.GitSHABefore} {
		if revision != "" && !slices.Contains(revisions, revision) {
			revisions = append(revisions, revision)
		}
	}
	return revisions
}

func promptPolicyHash(repoRoot string, prompt Prompt) (string, error) {
	if prompt.Role == "" {
		return "", nil
	}
	path := filepath.Join(repoRoot, ".promptgrinder", "roles", prompt.Role+".yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return hashBytes([]byte("missing role policy: " + prompt.Role)), nil
	}
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func refreshAdoptedFingerprints(sequence *SequenceState, prompts []Prompt, repoRoot string) error {
	if len(sequence.Items) != len(prompts) {
		return fmt.Errorf("cannot refresh adopted sequence %s fingerprints: item count changed after preflight", sequence.SequenceID)
	}
	for index, prompt := range prompts {
		rawHash, err := fileHash(prompt.Path)
		if err != nil {
			return err
		}
		policyHash, err := promptPolicyHash(repoRoot, prompt)
		if err != nil {
			return err
		}
		item := &sequence.Items[index]
		item.PromptPath = prompt.Path
		item.PromptID = prompt.ID
		item.DependsOn = append([]string(nil), prompt.DependsOn...)
		item.ContextMode = prompt.ContextMode
		item.PromptHash = rawHash
		item.PolicyHash = policyHash
		if prompt.RolePolicy != nil || prompt.Role == "" {
			effectiveHash, hashErr := promptContentHash(prompt.Path, prompt.RolePolicy)
			if hashErr != nil {
				return hashErr
			}
			item.ContentHash = effectiveHash
		}
	}
	sequence.StateVersion = sequenceStateVersion
	return nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateRemainingConfiguration(repoRoot string, prompts []Prompt, options Options) error {
	cfg, err := sequenceConfig(repoRoot, options)
	if err != nil {
		return err
	}
	for _, prompt := range prompts {
		if _, _, err := effectivePromptEngine(prompt.Path, cfg, options.EngineOverride); err != nil {
			return fmt.Errorf("validate current configuration for %s: %w", prompt.Name, err)
		}
	}
	return nil
}

func legacyMigrationError(sequenceID, reason string) error {
	return fmt.Errorf("cannot safely adopt legacy sequence %s: %s. This state predates explicit adoption fingerprints; restore its checkpoint/commit evidence or start a fresh sequence. PromptGrinder did not modify or clone the sequence", sequenceID, reason)
}
