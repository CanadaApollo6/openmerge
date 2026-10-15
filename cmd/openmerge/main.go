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
  version  Print version and build information
  help     Show this help
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
	default:
		_, _ = fmt.Fprintf(stderr, "Unknown command %q\n\n%s", strings.Join(args, " "), usage)
		return 2
	}
	if _, err := io.WriteString(stdout, output); err != nil {
		return 1
	}
	return 0
}
