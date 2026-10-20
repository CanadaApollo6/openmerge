package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func runAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "acknowledge" {
		_, _ = fmt.Fprintln(stderr, "Usage: openmerge admin acknowledge --queue <database queue ID> --event <outside push event ID> --sha <SHA> --operator <name> --drain-receipt <file> --backup-receipt <file>")
		return 2
	}
	flags := flag.NewFlagSet("admin acknowledge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	q := flags.Int64("queue", 0, "exact durable database queue ID (including retired queues)")
	source := flags.Int64("event", 0, "original outside_push_recorded event ID")
	sha := flags.String("sha", "", "original outside push new SHA")
	operator := flags.String("operator", "", "operator-supplied name; does not identify the pusher")
	db := flags.String("database-url", os.Getenv("OPENMERGE_DATABASE_URL"), "operator PostgreSQL URL (or OPENMERGE_DATABASE_URL)")
	timeout := flags.Duration("timeout", store.DefaultJournalReadTimeout, "complete command deadline (1ms..2m)")
	drain := flags.String("drain-receipt", "", "saved all-host old-worker drain/stop inventory (required for first-emission compatibility)")
	backup := flags.String("backup-receipt", "", "saved verified pre-emission backup/restore-test receipt (required)")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	candidate := queue.OutsidePushAcknowledged{SourceEventID: *source, SHA: *sha, Operator: *operator, At: time.Now().UTC()}
	if flags.NArg() != 0 || *q <= 0 || candidate.Validate() != nil || *db == "" || *timeout < time.Millisecond || *timeout > store.DefaultJournalReadTimeout {
		_, _ = fmt.Fprintln(stderr, "acknowledge requires --queue, --event, matching --sha, --operator, operator database access and a timeout of 1ms..2m")
		return 2
	}
	for _, path := range []string{*drain, *backup} {
		if err := adminReceiptFile(path); err != nil {
			_, _ = fmt.Fprintln(stderr, "first-emission gate: provide --drain-receipt and --backup-receipt as nonempty regular files; retain all-host worker inventory and a verified pre-emission backup restore test (docs/operating.md#acknowledging-outside-pushes)")
			return 2
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	s, err := store.OpenAdmin(ctx, *db, *timeout)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "open operator database:", err)
		return 1
	}
	defer s.Close()
	receipt, err := s.AcknowledgeOutsidePush(ctx, store.QueueID(*q), *source, *sha, *operator)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "acknowledge outside push:", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		_, _ = fmt.Fprintln(stderr, "write acknowledgement receipt:", err)
		return 1
	}
	return 0
}

func adminReceiptFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64*1024 {
		return errors.New("receipt must be a nonempty regular file up to 64KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64*1024 {
		return errors.New("receipt must be a nonempty regular file up to 64KiB")
	}
	var first [1]byte
	_, err = f.Read(first[:])
	return err
}
