package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/store"
)

func runAuthorityDowngradeCheck(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("admin downgrade-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	database := flags.String("database-url", "", "operator database URL; read-only, no migrations")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *database == "" || flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "downgrade-check requires --database-url with all workers stopped")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reader, err := store.OpenJournalReader(ctx, *database, 2*time.Minute)
	if err == nil {
		defer reader.Close()
		err = reader.CheckAuthorityDowngrade(ctx)
	}
	// Neither message echoes the error: it can carry connection details.
	if errors.Is(err, store.ErrAuthorityDowngradeRefused) {
		_, _ = fmt.Fprintln(stderr, "authority downgrade refused; retained journal/cursors require a compatible reader or a verified pre-emission restore and ref/evidence reconciliation")
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "authority downgrade check unavailable; nothing was decided; retry with the operator database reachable and all workers stopped")
		return 1
	}
	_, err = fmt.Fprintln(stdout, "ADR 0008 authority boundary absent; verify all other target-binary compatibility requirements before restart")
	if err != nil {
		return 1
	}
	return 0
}
