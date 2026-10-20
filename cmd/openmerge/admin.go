package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if err := adminReceiptFiles(*drain, *backup); err != nil {
		_, _ = fmt.Fprintln(stderr, "first-emission gate:", err, "(docs/operating.md#acknowledging-outside-pushes)")
		return 2
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

// Each receipt is an operator JSON document with its own kind and evidence.
func adminReceiptFiles(drain, backup string) error {
	var paths [2]string
	var hashes [2][32]byte
	var kinds [2]string
	for i, path := range []string{drain, backup} {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		paths[i], err = filepath.Abs(resolved)
		if err != nil {
			return err
		}
		info, err := os.Stat(paths[i])
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("receipt must be a regular file")
		}
		f, err := os.Open(paths[i])
		if err != nil {
			return err
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64*1024 {
			_ = f.Close()
			return errors.New("receipts must be nonempty regular JSON files up to 64KiB")
		}
		data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
		_ = f.Close()
		if err != nil {
			return err
		}
		if len(data) > 64*1024 {
			return errors.New("receipt exceeds 64KiB")
		}
		hashes[i] = sha256.Sum256(data)
		var receipt struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil {
			return fmt.Errorf("receipt must be a JSON document: %w", err)
		}
		kinds[i] = receipt.Kind
	}
	if paths[0] == paths[1] || hashes[0] == hashes[1] {
		return errors.New("drain and backup receipts must have distinct resolved paths and content hashes")
	}
	for i, kind := range []string{"drain", "backup"} {
		if kinds[i] != kind {
			return fmt.Errorf("%s receipt requires kind %q", kind, kind)
		}
	}
	return nil
}
