package firstuse

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupDryRunAndIdempotency(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	report, err := Setup(SetupOptions{HomeDir: home, DryRun: true})
	if err != nil || report.Changed {
		t.Fatalf("dry run = %#v, %v", report, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote home: %v", err)
	}
	report, err = Setup(SetupOptions{HomeDir: home, Yes: true})
	if err != nil || !report.Changed {
		t.Fatalf("setup = %#v, %v", report, err)
	}
	report, err = Setup(SetupOptions{HomeDir: home, NonInteractive: true})
	if err != nil || report.Changed {
		t.Fatalf("idempotent setup = %#v, %v", report, err)
	}
}

func TestSetupNeverOverwritesEditedConfigImplicitly(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.yaml")
	const edited = "custom: true\n"
	if err := os.WriteFile(configPath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Setup(SetupOptions{HomeDir: home, Yes: true})
	if err != nil {
		t.Fatalf("setup = %#v, %v", report, err)
	}
	data, _ := os.ReadFile(configPath)
	if string(data) != edited {
		t.Fatalf("config overwritten: %q", data)
	}
	if _, err := Setup(SetupOptions{HomeDir: home, Yes: true, Replace: true}); err == nil {
		t.Fatal("replace without backup must fail")
	}
	report, err = Setup(SetupOptions{HomeDir: home, Yes: true, Replace: true, Backup: true})
	if err != nil || !report.Changed {
		t.Fatalf("replacement = %#v, %v", report, err)
	}
	backup, _ := os.ReadFile(configPath + ".bak")
	if string(backup) != edited {
		t.Fatalf("backup = %q", backup)
	}
}

func TestSetupNonInteractiveRequiresYes(t *testing.T) {
	_, err := Setup(SetupOptions{HomeDir: filepath.Join(t.TempDir(), "home"), NonInteractive: true, Output: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v", err)
	}
}
