package main

import (
	"fmt"
	"os"
	"strings"

	"promptgrinder/internal/cli"
	"promptgrinder/internal/config"
	"promptgrinder/internal/roleenhance"
	pgruntime "promptgrinder/internal/runtime"
)

func main() {
	homeDir, err := config.ResolveHomeDir("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := config.Load("")
	if err != nil {
		if !isFirstUseCommand(os.Args[1:]) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// Doctor must remain available to explain malformed configuration, and
		// setup must remain available to repair first-run state.
		cfg = config.Config{HomeDir: homeDir, Engine: "codex", TerminalAdapter: "terminal"}
	}

	service := pgruntime.NewService(cfg)
	advisor := roleenhance.AiRoleAdvisor{Runtime: roleenhance.CodexRuntime{Executable: cfg.CodexExecutable}}
	root := cli.NewRootCommandWithRoleAdvisor(service, os.Stdout, os.Stderr, advisor)
	if err := root.Execute(); err != nil {
		if code, ok := cli.ExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func isFirstUseCommand(args []string) bool {
	for _, arg := range args {
		if arg == "doctor" || arg == "setup" {
			return true
		}
		if !strings.HasPrefix(arg, "-") {
			return false
		}
	}
	return false
}
