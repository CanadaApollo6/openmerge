package serve

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestStoredLegacyRulesLogRecoversEachSourceIndependently(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	stored, err := os.ReadFile("testdata/legacy_rules_log.jsonl")
	must(t, err)
	var events []queue.Event
	for _, data := range bytes.Split(bytes.TrimSpace(stored), []byte("\n")) {
		event, err := queue.DecodeEvent(data)
		must(t, err)
		events = append(events, event)
	}
	events = append(events,
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"repair classic protection"}},
		queue.RulesSourceBlocked{Source: queue.Permissions, Problems: []string{"missing contents: write"}},
	)
	_, err = s.store.Append(t.Context(), w.id, events, "")
	must(t, err)
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.RulesSourceRevision(queue.Rulesets) != 3 || !slices.Equal(state.RulesSourceProblems(queue.Rulesets), []string{"repair ruleset"}) {
		t.Fatal("legacy log changed replay semantics")
	}
	var logs strings.Builder
	s.options.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	w.CheckPermissions = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	w.CheckProtection = func(context.Context, forge.MergeMethod) ([]string, []string, error) {
		return nil, nil, errors.New("HTTP 403; Administration read unavailable")
	}
	restarted, err := New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	s, w = restarted, restarted.queues[0]
	state, watermark, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.RulesSourceRevision(queue.Rulesets) != 4 || len(state.RulesSourceProblems(queue.Rulesets)) != 0 || state.RulesSourceRevision(queue.Permissions) != 2 || len(state.RulesSourceProblems(queue.Permissions)) != 0 || state.RulesSourceRevision(queue.ClassicProtection) != 1 || !slices.Equal(state.RulesProblems(), []string{"repair classic protection"}) {
		t.Fatalf("unreadable classic protection vetoed another source's recovery: %+v", state.RulesSources())
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "source=classic_protection") {
		t.Fatalf("unreadable source missing WARN: %s", &logs)
	}
	for range 2 {
		w.rulesNextCheck = time.Time{}
		must(t, s.repositoryRules(t.Context(), w))
	}
	_, unchanged, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if watermark != unchanged {
		t.Fatal("unreadable source re-appended another source's previous block")
	}
	w.CheckProtection = func(context.Context, forge.MergeMethod) ([]string, []string, error) { return nil, nil, nil }
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if len(state.RulesProblems()) > 0 || state.RulesSourceRevision(queue.ClassicProtection) != 2 {
		t.Fatal("readable classic protection did not recover independently")
	}
	// Restart with the default manifest's unreadable classic endpoint: no prior
	// ruleset/permission block can reappear or stop a recovered queue.
	w.CheckProtection = func(context.Context, forge.MergeMethod) ([]string, []string, error) {
		return nil, nil, errors.New("HTTP 403")
	}
	restarted, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
	must(t, err)
	state, _, err = restarted.store.Load(t.Context(), w.id)
	must(t, err)
	if len(state.RulesProblems()) != 0 {
		t.Fatal("restart restored a cleared source block")
	}
}

func TestRulesPartialFailuresRetainAndRecordDefiniteFindings(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"old signature block"}},
		queue.RulesSourceBlocked{Source: queue.Rulesets, Problems: []string{"old ruleset block"}},
	}, "")
	must(t, err)
	w.CheckRules = func(context.Context) ([]string, []string, error) { return nil, nil, nil }
	w.CheckProtection = func(context.Context, forge.MergeMethod) ([]string, []string, error) {
		return []string{"definite review block"}, nil, errors.New("fence unreadable")
	}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if !slices.Equal(state.RulesSourceProblems(queue.ClassicProtection), []string{"definite review block", "old signature block"}) || state.RulesSourceRevision(queue.ClassicProtection) != 2 || len(state.RulesSourceProblems(queue.Rulesets)) > 0 {
		t.Fatalf("partial findings lost or crossed sources: %+v", state.RulesSources())
	}
	// The complete read may remove a no-longer-present finding; an errored read cannot.
	w.CheckProtection = func(context.Context, forge.MergeMethod) ([]string, []string, error) {
		return []string{"definite review block"}, nil, nil
	}
	w.rulesNextCheck = time.Time{}
	must(t, s.repositoryRules(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if !slices.Equal(state.RulesProblems(), []string{"definite review block"}) {
		t.Fatal("complete observation retained resolved partial findings")
	}
}

func TestOperationalLogsIgnoreStaleSourceClears(t *testing.T) {
	s, w, _, _ := fixture(t, repositoryConfig)
	var output bytes.Buffer
	s.options.Logger = slog.New(slog.NewJSONHandler(&output, nil))
	_, err := s.store.Append(t.Context(), w.id, []queue.Event{
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"original"}},
		queue.RulesSourceBlocked{Source: queue.ClassicProtection, Problems: []string{"newer"}, Basis: 1},
		queue.RulesSourceCleared{Source: queue.ClassicProtection, Basis: 1},
	}, "")
	must(t, err)
	s.logCommitted(t.Context(), w)
	if strings.Contains(output.String(), "cleared") || strings.Contains(output.String(), "resumed") || !strings.Contains(output.String(), `"source":"classic_protection"`) {
		t.Fatalf("incorrect source transition logs: %s", &output)
	}
}
