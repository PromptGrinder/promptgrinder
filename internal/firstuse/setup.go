package firstuse

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"promptgrinder/internal/config"
)

type Mutation struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Status string `json:"status"`
}

type SetupReport struct {
	OK           bool          `json:"ok"`
	DryRun       bool          `json:"dry_run"`
	Changed      bool          `json:"changed"`
	Capabilities *DoctorReport `json:"capabilities,omitempty"`
	Mutations    []Mutation    `json:"mutations"`
}

type SetupOptions struct {
	HomeDir        string
	DryRun         bool
	NonInteractive bool
	Yes            bool
	Replace        bool
	Backup         bool
	Input          io.Reader
	Output         io.Writer
}

func Setup(options SetupOptions) (SetupReport, error) {
	report := SetupReport{OK: true, DryRun: options.DryRun}
	if options.HomeDir == "" {
		var err error
		options.HomeDir, err = config.ResolveHomeDir("")
		if err != nil {
			return report, err
		}
	}
	if options.Input == nil {
		options.Input = strings.NewReader("")
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.Replace && !options.Backup {
		return report, fmt.Errorf("replacing an existing config requires an explicit backup")
	}
	dirs := []string{
		options.HomeDir,
		filepath.Join(options.HomeDir, "workers"),
		filepath.Join(options.HomeDir, "sequences"),
		filepath.Join(options.HomeDir, "templates"),
	}
	configPath := filepath.Join(options.HomeDir, "config.yaml")
	for _, path := range dirs {
		action := "create_directory"
		status := "planned"
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			action, status = "keep_directory", "unchanged"
		}
		report.Mutations = append(report.Mutations, Mutation{Path: path, Action: action, Status: status})
	}
	configExists := false
	if _, err := os.Stat(configPath); err == nil {
		configExists = true
	}
	configAction := "create_config"
	configStatus := "planned"
	if configExists && !options.Replace {
		configAction, configStatus = "keep_config", "unchanged"
	}
	if configExists && options.Replace {
		configAction = "replace_config"
	}
	report.Mutations = append(report.Mutations, Mutation{Path: configPath, Action: configAction, Status: configStatus})

	for _, mutation := range report.Mutations {
		fmt.Fprintf(options.Output, "%s: %s (%s)\n", mutation.Action, mutation.Path, mutation.Status)
	}
	if options.DryRun {
		return report, nil
	}
	planned := false
	for _, mutation := range report.Mutations {
		planned = planned || mutation.Status == "planned"
	}
	if planned && !options.Yes {
		if options.NonInteractive {
			return report, fmt.Errorf("setup requires --yes in non-interactive mode; no files were changed")
		}
		fmt.Fprint(options.Output, "Apply these PromptGrinder-owned changes? [y/N] ")
		answer, err := bufio.NewReader(options.Input).ReadString('\n')
		if err != nil && err != io.EOF {
			return report, err
		}
		if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
			return report, fmt.Errorf("setup cancelled; no files were changed")
		}
	}
	for i := range report.Mutations {
		mutation := &report.Mutations[i]
		var backupMutation *Mutation
		if mutation.Status != "planned" {
			continue
		}
		switch mutation.Action {
		case "create_directory":
			if err := os.MkdirAll(mutation.Path, 0o700); err != nil {
				report.OK = false
				return report, err
			}
		case "create_config", "replace_config":
			if mutation.Action == "replace_config" && options.Backup {
				backupPath := mutation.Path + ".bak"
				if _, err := os.Stat(backupPath); err == nil {
					return report, fmt.Errorf("backup already exists at %s; no config was replaced", backupPath)
				}
				data, err := os.ReadFile(mutation.Path)
				if err != nil {
					return report, err
				}
				if err := os.WriteFile(backupPath, data, 0o600); err != nil {
					return report, err
				}
				backupMutation = &Mutation{Path: backupPath, Action: "backup_config", Status: "created"}
			}
			if err := os.WriteFile(mutation.Path, []byte(minimalConfig), 0o600); err != nil {
				report.OK = false
				return report, err
			}
		}
		mutation.Status = "created"
		if mutation.Action == "replace_config" {
			mutation.Status = "replaced"
		}
		if backupMutation != nil {
			report.Mutations = append(report.Mutations, *backupMutation)
		}
		report.Changed = true
	}
	return report, nil
}

const minimalConfig = `# PromptGrinder user configuration.
# Run "promptgrinder doctor" after editing.
engine:
  default: codex

terminal:
  adapter: terminal

worker:
  heartbeat_interval: 30s
`
