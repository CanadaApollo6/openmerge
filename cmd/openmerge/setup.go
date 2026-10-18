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
  openmerge setup import-protection --repo owner/name [--base BRANCH...] [--accept ID...] [--json] [--out FILE]

App setup defaults to the signed-in personal account and polling (webhooks disabled).
The manifest requests Actions read/write for optional CI retries and cancellation.
The config directory must not exist; defaults to $XDG_CONFIG_HOME/openmerge/<slug>/.
Rulesets require GITHUB_TOKEN from a repository admin, with Administration read
for inspection or write for --apply. Without --apply, only a preview is printed.
Import-protection only makes GET requests; it also needs Contents, Checks and
Commit statuses read. --out saves draft fragments locally. Exit 1 means a read
failed or policy gaps/installation prerequisites remain; --accept only waives
weaker/unsupported policy items, never installation prerequisites.
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
	var protection github.ImportOptions
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
	case "import-protection":
		flags.StringVar(&protection.Repo, "repo", "", "repository owner/name")
		flags.Func("base", "additional concrete base branch (repeatable)", func(value string) error { protection.Bases = append(protection.Bases, value); return nil })
		flags.Func("accept", "policy relaxation item ID (repeatable; cannot waive installation prerequisites)", func(value string) error { protection.Accept = append(protection.Accept, value); return nil })
		flags.BoolVar(&protection.JSON, "json", false, "print the versioned JSON report")
		flags.StringVar(&protection.Out, "out", "", "write proposed configuration fragments to a local file")
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
	switch args[0] {
	case "github":
		err = github.SetupApp(ctx, manifest, stdout)
	case "import-protection":
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		var report github.ProtectionReport
		report, err = github.ImportProtection(ctx, protection, os.Getenv("GITHUB_TOKEN"))
		if err == nil {
			err = report.Write(stdout, protection.JSON)
		}
		if err == nil && protection.Out != "" {
			err = os.WriteFile(protection.Out, []byte(report.ProposedConfig), 0o600)
		}
		if err == nil && !report.Ready {
			err = errors.New("protection import needs attention: accept every weaker/unsupported item and resolve all installation blockers")
		}
	case "rulesets":
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
