package roleenhance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"promptgrinder/internal/workerlaunch"
)

// CodexRuntime is the production adapter for the runtime-neutral advisor
// boundary. Codex receives only the bounded request on stdin and runs in a
// temporary directory with a read-only sandbox; it never receives the
// repository path.
type CodexRuntime struct {
	Executable string
}

func (CodexRuntime) Capabilities() workerlaunch.Capabilities {
	return workerlaunch.Capabilities{Headless: true, StructuredOutput: true, Sandbox: true}
}

func (r CodexRuntime) Advise(ctx context.Context, request []byte) ([]byte, error) {
	executable := r.Executable
	if executable == "" {
		executable = "codex"
	}
	temp, err := os.MkdirTemp("", "promptgrinder-role-advisor-")
	if err != nil {
		return nil, fmt.Errorf("create advisor temporary directory: %w", err)
	}
	defer os.RemoveAll(temp)
	schemaPath := filepath.Join(temp, "schema.json")
	outputPath := filepath.Join(temp, "response.json")
	if err := os.WriteFile(schemaPath, []byte(advisorResponseJSONSchema), 0o600); err != nil {
		return nil, fmt.Errorf("write advisor schema: %w", err)
	}
	prompt := append([]byte("Return only recommendations supported by the supplied evidence. Do not infer technologies or paths. The JSON request follows.\n"), request...)
	cmd := exec.CommandContext(ctx, executable, "exec", "--sandbox", "read-only", "--skip-git-repo-check", "--output-schema", schemaPath, "--output-last-message", outputPath, "-")
	cmd.Dir = temp
	cmd.Stdin = bytes.NewReader(prompt)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("codex advisor execution: %w", err)
	}
	response, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read codex advisor response: %w", err)
	}
	return response, nil
}

const advisorResponseJSONSchema = `{
  "type":"object",
  "additionalProperties":false,
  "required":["schema_version","recommendations"],
  "properties":{
    "schema_version":{"const":"promptgrinder.role-advisor/v1"},
    "recommendations":{"type":"array","items":{
      "type":"object","additionalProperties":false,
      "required":["id","role_id","operation","field","value","confidence","explanation","evidence"],
      "properties":{
        "id":{"type":"string"},"role_id":{"type":"string"},
        "operation":{"enum":["set","append","remove"]},"field":{"type":"string"},
        "value":{"oneOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]},
        "confidence":{"enum":["low","medium","high"]},"explanation":{"type":"string"},
        "evidence":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string"},"fact":{"type":"string"}}}}
      }
    }}
  }
}`
