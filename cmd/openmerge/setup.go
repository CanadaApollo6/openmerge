package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge/github"
)

const setupUsage = `Usage:
  openmerge setup github [--owner ORG | --user] [--repo owner/name] [--name NAME] [--config-dir DIR] [--webhook-url URL]
  openmerge setup rulesets --repo owner/name (--app-id ID | --config-dir DIR) [--apply]

App setup defaults to the signed-in personal account and polling (webhooks disabled).
The config directory must not exist; defaults to $XDG_CONFIG_HOME/openmerge/<slug>/.
Rulesets require GITHUB_TOKEN from a repository admin, with Administration read
for inspection or write for --apply. Without --apply, only a preview is printed.
`

func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if _, err := io.WriteString(stdout, setupUsage); err != nil {
			return 1
		}
		return 0
	}
	flags := flag.NewFlagSet("setup "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, setupUsage) }
	var manifest github.ManifestOptions
	var rules github.RulesOptions
	var user bool
	var configDir string
	switch args[0] {
	case "github":
		flags.StringVar(&manifest.Owner, "owner", "", "organization that will own the App")
		flags.BoolVar(&user, "user", false, "create under the signed-in personal account (default)")
		flags.StringVar(&manifest.Repo, "repo", "", "repository for installation guidance and next-step commands")
		flags.StringVar(&manifest.Name, "name", "", "App name (editable on GitHub)")
		flags.StringVar(&manifest.ConfigDir, "config-dir", "", "new directory for credentials")
		flags.StringVar(&manifest.WebhookURL, "webhook-url", "", "enable delivery to this public HTTPS URL")
	case "rulesets":
		flags.StringVar(&rules.Repo, "repo", "", "repository owner/name")
		flags.Int64Var(&rules.AppID, "app-id", 0, "App ID (not installation ID)")
		flags.StringVar(&configDir, "config-dir", "", "App directory containing app-id")
		flags.BoolVar(&rules.Apply, "apply", false, "apply the printed changes")
	default:
		_, _ = io.WriteString(stderr, setupUsage)
		return 2
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || user && manifest.Owner != "" {
		_, _ = fmt.Fprintln(stderr, "setup accepts no positional arguments; --owner and --user are mutually exclusive")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if args[0] == "github" {
		err = github.SetupApp(ctx, manifest, stdout)
	} else {
		if configDir != "" {
			if rules.AppID != 0 {
				_, _ = fmt.Fprintln(stderr, "use either --app-id or --config-dir")
				return 2
			}
			var data []byte
			data, err = os.ReadFile(filepath.Join(configDir, "app-id"))
			if err == nil {
				rules.AppID, err = strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			}
			if err != nil {
				_, _ = fmt.Fprintln(stderr, "cannot read a numeric App ID from --config-dir")
				return 1
			}
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		err = github.SetupRules(ctx, rules, os.Getenv("GITHUB_TOKEN"), stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
