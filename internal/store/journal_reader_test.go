package store

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestJournalReaderDoesNotMigrateAnEmptyDatabase(t *testing.T) {
	url := databaseURL(t)
	r, err := OpenJournalReader(t.Context(), url, DefaultJournalReadTimeout)
	must(t, err)
	defer r.Close()
	_, err = r.Journals(t.Context(), []QueueKey{{Installation: "1", Repository: "o/r", BaseBranch: "main"}})
	if err == nil {
		t.Fatal("reading an unmigrated database should fail")
	}
	tx, err := r.store.pool.BeginTx(t.Context(), pgx.TxOptions{AccessMode: pgx.ReadOnly})
	must(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	var table *string
	must(t, tx.QueryRow(t.Context(), "SELECT to_regclass('queues')::text").Scan(&table))
	if table != nil {
		t.Fatal("read-only opener created schema")
	}
	var mode string
	must(t, tx.QueryRow(t.Context(), "SHOW transaction_read_only").Scan(&mode))
	if mode != "on" {
		t.Fatalf("transaction_read_only=%s", mode)
	}
}
func TestJournalReaderReadsExistingJournalWithoutMutations(t *testing.T) {
	url := databaseURL(t)
	s := openTestStore(t, url)
	key := QueueKey{Installation: "1", Repository: "o/r", BaseBranch: "main"}
	id, err := s.EnsureQueue(t.Context(), key)
	must(t, err)
	appendEvents(t, s, id, seedEvents()...)
	r, err := OpenJournalReader(t.Context(), url, DefaultJournalReadTimeout)
	must(t, err)
	defer r.Close()
	journals, err := r.Journals(t.Context(), []QueueKey{key})
	must(t, err)
	if len(journals) != 1 || len(journals[0].Events) != 3 {
		t.Fatal(journals)
	}
	// Even a transaction without explicit options inherits a read-only connection.
	tx, err := r.store.pool.Begin(t.Context())
	must(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), "DELETE FROM events")
	if err == nil {
		t.Fatal("read-only reader allowed a write")
	}
}
