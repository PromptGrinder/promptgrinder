package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"promptgrinder/internal/engine"
)

// ListModels asks the installed Codex app server for the active account's
// model catalog. The app server, rather than a bundled list, is authoritative
// because eligibility varies with login, provider, and CLI version.
func (e Engine) ListModels(ctx context.Context) ([]engine.Model, error) {
	command := exec.CommandContext(ctx, e.command(), "app-server", "--stdio")
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
	}()

	encoder := json.NewEncoder(stdin)
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "promptgrinder", "version": "v1"}, "capabilities": map[string]any{"experimentalApi": true}}}); err != nil {
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if _, err := readResponse(scanner, 1); err != nil {
		return nil, catalogError(err, stderr.String())
	}
	models := []engine.Model{}
	seen := map[string]bool{}
	cursor := ""
	for requestID := 2; requestID < 102; requestID++ {
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "model/list", "params": params}); err != nil {
			return nil, fmt.Errorf("request Codex model catalog: %w", err)
		}
		result, err := readResponse(scanner, requestID)
		if err != nil {
			return nil, catalogError(err, stderr.String())
		}
		var catalog struct {
			Data []struct {
				ID              string   `json:"id"`
				Model           string   `json:"model"`
				InputModalities []string `json:"inputModalities"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &catalog); err != nil {
			return nil, fmt.Errorf("decode Codex model catalog: %w", err)
		}
		for _, item := range catalog.Data {
			name := strings.TrimSpace(item.Model)
			if name == "" {
				name = strings.TrimSpace(item.ID)
			}
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			models = append(models, engine.Model{ID: name, InputModalities: append([]string(nil), item.InputModalities...)})
		}
		if catalog.NextCursor == nil || strings.TrimSpace(*catalog.NextCursor) == "" {
			break
		}
		cursor = *catalog.NextCursor
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Codex model catalog returned no selectable models")
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func readResponse(scanner *bufio.Scanner, wantID int) (json.RawMessage, error) {
	for scanner.Scan() {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || len(response.ID) == 0 {
			continue
		}
		var id int
		if json.Unmarshal(response.ID, &id) != nil || id != wantID {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("Codex app-server: %s", response.Error.Message)
		}
		if len(response.Result) == 0 {
			return nil, fmt.Errorf("Codex app-server returned no result")
		}
		return response.Result, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("Codex app-server closed before replying")
}

func catalogError(err error, stderr string) error {
	if text := strings.TrimSpace(stderr); text != "" {
		return fmt.Errorf("%w (%s)", err, text)
	}
	return err
}
