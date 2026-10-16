// Command openmerge is the OpenMerge command-line interface.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
)

var version = "dev"

const usage = `Usage: openmerge <command>

Commands:
  version                 Print version and build information
  config validate [path]  Validate repository configuration
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
		release, commit, goVersion := version, "unknown", "unknown"
		if info, ok := debug.ReadBuildInfo(); ok {
			goVersion = info.GoVersion
			if release == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
				release = info.Main.Version
			}
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					commit = setting.Value
				}
			}
		}
		output = fmt.Sprintf("openmerge %s\ncommit: %s\ngo: %s\n", release, commit, goVersion)
	case (len(args) == 2 || len(args) == 3) && args[0] == "config" && args[1] == "validate":
		return validateConfig(args[2:], stdout, stderr)
	case len(args) > 0 && args[0] == "serve":
		return runServe(args[1:], stdout, stderr)
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
