// Command openmerge is the OpenMerge command-line interface.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/schema"
)

var version = "dev"

const usage = `Usage: openmerge <command>

Commands:
  version                 Print version and build information
  config validate [path]  Validate repository configuration
  config schema           Print the version 1 JSON Schema
  change show <id>        Read recorded change status
  change brief <id>       Read a recorded change brief
  change decisions <id>   Read recorded landing decisions
  queue status [options]  Read queue status and PR explanations
  admin acknowledge       Record operator awareness of an outside push
  serve [options]         Run the GitHub merge queue server
  setup <github|rulesets>  Set up the GitHub App and repository rules
  sim [options]           Simulate a day of merge queue traffic
  help                    Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var output string
	switch {
	case len(args) == 0, len(args) == 1 && args[0] == "help":
		output = usage
	case len(args) == 1 && args[0] == "version":
		info, _ := debug.ReadBuildInfo()
		output = versionOutput(info, authority.BuildRevision)
	case len(args) == 2 && args[0] == "config" && args[1] == "schema":
		output = schema.V1
	case (len(args) == 2 || len(args) == 3) && args[0] == "config" && args[1] == "validate":
		return validateConfig(args[2:], stdout, stderr)
	case len(args) > 0 && args[0] == "admin":
		return runAdmin(args[1:], stdout, stderr)
	case len(args) > 0 && args[0] == "serve":
		return runServe(args[1:], stdout, stderr)
	case len(args) > 0 && args[0] == "change":
		return runChange(args[1:], stdout, stderr)
	case len(args) > 0 && args[0] == "queue":
		return runQueue(args[1:], stdout, stderr)
	case len(args) > 0 && args[0] == "setup":
		return runSetup(args[1:], stdout, stderr)
	case len(args) > 0 && args[0] == "sim":
		return runSim(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "Unknown command %q\n\n%s", strings.Join(args, " "), usage)
		return 2
	}
	if _, err := io.WriteString(stdout, output); err != nil {
		return 1
	}
	return 0
}

func versionOutput(info *debug.BuildInfo, fallback string) string {
	release, goVersion := version, "unknown"
	if info != nil {
		goVersion = info.GoVersion
		if release == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			release = info.Main.Version
		}
	}
	commit, attestable := authority.EmbeddedBinaryRevision(info, fallback)
	if commit == "" {
		commit = "unknown"
	}
	return fmt.Sprintf("openmerge %s\ncommit: %s\nattestable: %t\ngo: %s\n", release, commit, attestable, goVersion)
}
