package serve

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

func TestStageACheckSharesLedgerSnapshotAndLogsUnavailablePolicy(t *testing.T) {
	t.Parallel()
	s, w, _ := shadowFixture(t, 1, "main", "report-cache", false)
	_, err := s.ledgerSummary(t.Context(), w, 1)
	must(t, err)
	cached := w.ledgerCache
	var logs bytes.Buffer
	s.options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	// A mismatched policy must produce honest unavailable evidence. Once the
	// ledger is cached the check formatter needs no second store read at all.
	w.stageA.policy = cp.Policy{Approvals: &cp.Approvals{Default: cp.Requirement{Selector: cp.Human, Count: 99}}}
	database := s.store
	s.store = nil
	text, err := s.stageACheckSummary(t.Context(), w, 1)
	s.store = database
	must(t, err)
	if w.ledgerCache != cached || !strings.Contains(text, "policy\\_unavailable") || !strings.Contains(logs.String(), "check approval policy unavailable") || !strings.Contains(logs.String(), "error=") {
		t.Fatalf("missing visible degradation: %s logs=%s", text, logs.String())
	}
}
