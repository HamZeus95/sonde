// Command sonde tests operational runbooks against live infrastructure.
//
// Every check it runs is read-only. It never executes a runbook's steps.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Exit codes are contractual: CI pipelines branch on them, so they may not
// change casually.
//
//	0  all checks passed
//	1  one or more checks failed — the runbook is wrong
//	2  usage error — bad flags or arguments
//	3  parse error in one or more runbooks
//	4  execution error — could not reach the target infrastructure
const (
	exitOK        = 0
	exitFailed    = 1
	exitUsage     = 2
	exitParse     = 3
	exitExecution = 4
)

// exitError carries the exit code a failure should produce. Anything else is a
// usage error, because a command that cannot say what went wrong has not
// thought about it.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func exit(code int, err error) error { return &exitError{code: code, err: err} }

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var exitErr *exitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.code)
		}
		os.Exit(exitUsage)
	}
	os.Exit(exitOK)
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "sonde",
		Short: "Test operational runbooks against live infrastructure",
		Long: "Sonde is a test harness for operational runbooks.\n\n" +
			"Runbooks make factual claims about live infrastructure: that a deployment\n" +
			"exists, that a dashboard link resolves, that the on-call group can scale a\n" +
			"service. Sonde executes those claims read-only and reports which ones have\n" +
			"stopped being true.",
		Version: buildVersion(),
		// Errors are printed once, by main, with the right exit code.
		SilenceErrors: true,
		// Usage is printed for usage errors only, not for a runbook that failed.
		SilenceUsage: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(newParseCommand())
	root.AddCommand(newCheckCommand())
	root.AddCommand(newProbeCommand())
	root.AddCommand(newImpactCommand())
	root.AddCommand(newSuggestCommand())
	root.AddCommand(newVerifyCommand())
	return root
}

// version is stamped in at build time by the container image and the release
// workflow; a `go build` with no ldflags falls back to what the toolchain
// recorded.
var version string

// buildVersion reports what this binary is.
func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "dev"
	}
	return info.Main.Version
}
