package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/audit"
)

const auditUsage = `Usage: openmerge audit export --repo owner/name --installation <id> --from <RFC3339> --until <RFC3339> --out <directory> [--version 1|2]
       openmerge audit export --manifest <manifest.json> --out <directory>

Reads OPENMERGE_DATABASE_URL using a read-only snapshot. The output directory
must not exist. Manifest mode re-exports the exact saved selection and rejects
range selectors and --version. Default exports use v1 for legacy-only closure,
v2 for provenance; explicit v1 refuses provenance. --timeout defaults to 5m. Linux is required. Exit codes: 0 success; 2 usage/selectors/bounds; 1 source,
integrity, connection or publication failure. No migrations or forge calls.
`

var auditRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func runAudit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		_, err := io.WriteString(stdout, auditUsage)
		if err != nil {
			return 1
		}
		return 0
	}
	if len(args) == 0 || args[0] != "export" {
		_, _ = io.WriteString(stderr, auditUsage)
		return 2
	}
	flags := flag.NewFlagSet("audit export", flag.ContinueOnError)
	// Flag errors can contain arbitrary arguments, including accidentally pasted
	// credentials. Emit only fixed diagnostics, including for parse failures.
	flags.SetOutput(io.Discard)
	repo := flags.String("repo", "", "owner/name")
	installation := flags.String("installation", "", "installation ID")
	from := flags.String("from", "", "inclusive RFC3339 bound")
	until := flags.String("until", "", "exclusive RFC3339 bound")
	out := flags.String("out", "", "new output directory")
	timeout := flags.Duration("timeout", 5*time.Minute, "snapshot timeout")
	version := flags.Int("version", 0, "export version: 1 or 2; default automatic")
	manifest := flags.String("manifest", "", "saved manifest for exact re-export")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(stdout, auditUsage)
		if err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		_, _ = fmt.Fprintln(stderr, "audit: invalid command flags")
		return 2
	}
	selectors, manifestSet, versionSet := false, false, false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "repo", "installation", "from", "until":
			selectors = true
		case "manifest":
			manifestSet = true
		case "version":
			versionSet = true
		}
	})
	invalid := *timeout < time.Millisecond || flags.NArg() != 0 || *out == "" || manifestSet && (*manifest == "" || selectors || versionSet) || versionSet && *version != 1 && *version != 2
	if !manifestSet {
		id, err := strconv.ParseInt(*installation, 10, 64)
		invalid = invalid || err != nil || id <= 0 || strconv.FormatInt(id, 10) != *installation || !auditRepo.MatchString(*repo)
		parts := strings.Split(*repo, "/")
		for _, part := range parts {
			invalid = invalid || part == "." || part == ".."
		}
		start, e1 := time.Parse(time.RFC3339Nano, *from)
		end, e2 := time.Parse(time.RFC3339Nano, *until)
		invalid = invalid || e1 != nil || e2 != nil || start.After(end) || start.UTC().Year() < 1 || end.UTC().Year() < 1 || start.UTC().Year() > 9999 || end.UTC().Year() > 9999
	}
	if invalid {
		_, _ = fmt.Fprintln(stderr, "audit: invalid selectors, bounds or output; see audit --help")
		return 2
	}
	if runtime.GOOS != "linux" {
		_, _ = fmt.Fprintln(stderr, "audit: atomic publication requires Linux")
		return 1
	}
	if os.Getenv("OPENMERGE_DATABASE_URL") == "" {
		return auditFailure(stderr, nil)
	}
	reader, err := audit.Open(context.Background(), os.Getenv("OPENMERGE_DATABASE_URL"), *timeout)
	if err != nil {
		return auditFailure(stderr, err)
	}
	defer reader.Close()
	var result audit.Manifest
	if manifestSet {
		result, err = reader.Replay(context.Background(), *manifest, *out)
	} else {
		result, err = reader.ExportVersion(context.Background(), audit.Request{Installation: *installation, Repository: *repo, From: *from, Until: *until}, *out, *version)
	}
	if err != nil {
		return auditFailure(stderr, err)
	}
	if _, err = fmt.Fprintf(stdout, "%s\nmanifest SHA-256: %s\n", *out, result.ManifestSHA256); err != nil {
		return 1
	}
	return 0
}

func auditFailure(stderr io.Writer, err error) int {
	if errors.Is(err, audit.ErrSelectors) {
		_, _ = fmt.Fprintln(stderr, "audit: invalid selectors or bounds")
		return 2
	} else if errors.Is(err, audit.ErrProvenanceVersion) {
		_, _ = fmt.Fprintln(stderr, "audit: --version 1 cannot include provenance; use --version 2 or omit --version")
	} else if errors.Is(err, audit.ErrTimeout) {
		_, _ = fmt.Fprintln(stderr, "audit: export timed out")
	} else if errors.Is(err, audit.ErrPinRejected) {
		_, _ = fmt.Fprintln(stderr, "audit: governing authority pin rejected by queue fold")
	} else {
		_, _ = fmt.Fprintln(stderr, "audit: connection, source, integrity or publication failure")
	}
	return 1
}
