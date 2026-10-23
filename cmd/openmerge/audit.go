package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/audit"
)

const auditUsage = `Usage: openmerge audit export --repo owner/name --installation <id> --from <RFC3339> --until <RFC3339> --out <directory>
       openmerge audit export --manifest <manifest.json> --out <directory>

Reads OPENMERGE_DATABASE_URL using a read-only snapshot. The output directory
must not exist. Manifest mode re-exports the exact saved selection and rejects
range selectors. Exit codes: 0 success; 2 usage/selectors/bounds; 1 source,
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
	selectors, manifestSet := false, false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "repo", "installation", "from", "until":
			selectors = true
		case "manifest":
			manifestSet = true
		}
	})
	invalid := flags.NArg() != 0 || *out == "" || manifestSet && (*manifest == "" || selectors)
	if !manifestSet {
		id, err := strconv.ParseInt(*installation, 10, 64)
		invalid = invalid || err != nil || id <= 0 || strconv.FormatInt(id, 10) != *installation || !auditRepo.MatchString(*repo)
		parts := strings.Split(*repo, "/")
		for _, part := range parts {
			invalid = invalid || part == "." || part == ".."
		}
		start, e1 := time.Parse(time.RFC3339Nano, *from)
		end, e2 := time.Parse(time.RFC3339Nano, *until)
		invalid = invalid || e1 != nil || e2 != nil || start.After(end)
	}
	if invalid {
		_, _ = fmt.Fprintln(stderr, "audit: invalid selectors, bounds or output; see audit --help")
		return 2
	}
	if os.Getenv("OPENMERGE_DATABASE_URL") == "" {
		return auditFailure(stderr, nil)
	}
	reader, err := audit.Open(context.Background(), os.Getenv("OPENMERGE_DATABASE_URL"), 5*time.Minute)
	if err != nil {
		return auditFailure(stderr, err)
	}
	defer reader.Close()
	if manifestSet {
		_, err = reader.Replay(context.Background(), *manifest, *out)
	} else {
		_, err = reader.Export(context.Background(), audit.Request{Installation: *installation, Repository: *repo, From: *from, Until: *until}, *out)
	}
	if err != nil {
		return auditFailure(stderr, err)
	}
	return 0
}

func auditFailure(stderr io.Writer, err error) int {
	if errors.Is(err, audit.ErrPinRejected) {
		_, _ = fmt.Fprintln(stderr, "audit: governing authority pin rejected by queue fold")
	} else {
		_, _ = fmt.Fprintln(stderr, "audit: connection, source, integrity or publication failure")
	}
	return 1
}
