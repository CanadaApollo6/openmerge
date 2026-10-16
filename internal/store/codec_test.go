package store

import (
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

var epoch = time.Date(2026, 10, 15, 12, 0, 0, 123456789, time.UTC)
var config = queue.Config{MergeMethod: queue.Squash, CheckTimeout: time.Minute, ResolveAfter: time.Minute}

// Type checking enumerates concrete implementations, including implementations
// inherited through embedding. A new queue variant fails until registered here.
func TestCodecRegistryCoversQueueVariants(t *testing.T) {
	fset := token.NewFileSet()
	paths, err := filepath.Glob("../queue/*.go")
	must(t, err)
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		must(t, err)
		files = append(files, file)
	}
	cfg := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := cfg.Check("github.com/CanadaApollo6/openmerge/internal/queue", fset, files, nil)
	must(t, err)
	for _, tc := range []struct {
		name       string
		registered map[string]bool
	}{
		{"Event", registeredNames(eventTypes)},
		{"Action", registeredNames(actionTypes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			iface := pkg.Scope().Lookup(tc.name).Type().Underlying().(*types.Interface)
			found := make(map[string]bool)
			for _, name := range pkg.Scope().Names() {
				obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
				if !ok || obj.IsAlias() {
					continue
				}
				if _, ok := obj.Type().Underlying().(*types.Interface); ok {
					continue
				}
				if types.Implements(obj.Type(), iface) || types.Implements(types.NewPointer(obj.Type()), iface) {
					found[name] = true
					if !tc.registered[name] {
						t.Errorf("queue.%s implements %s but has no codec entry", name, tc.name)
					}
				}
			}
			if !reflect.DeepEqual(found, tc.registered) {
				t.Errorf("registry mismatch: queue %v, codec %v", found, tc.registered)
			}
		})
	}
}

func registeredNames[T any](registry map[string]T) map[string]bool {
	result := make(map[string]bool)
	for _, value := range registry {
		result[reflect.TypeOf(value).Name()] = true
	}
	return result
}

func eventSamples() []queue.Event {
	return []queue.Event{
		queue.PRObserved{PR: 2, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.BaseMoved{SHA: "base"}, queue.ChecksCompleted{TestedSHA: "tested", Success: true},
		queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: epoch}, queue.PRClosed{PR: 2},
		queue.EnqueueRequested{PR: 2, At: epoch}, queue.DequeueRequested{PR: 2}, queue.ResumeRequested{},
		queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: epoch, Method: queue.Squash},
		queue.TestRefBuildFailed{BatchID: "b1", Conflict: true, At: epoch},
		queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: epoch},
		queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"},
		queue.LandingAbandoned{BatchID: "b1", NewFence: "next"},
		queue.Dequeued{PR: 2, AdmissionID: 3, Reason: queue.Requested, BatchID: "b1"},
		queue.ActionCompleted{Key: "effect"},
	}
}

func TestEventCodecRoundTrip(t *testing.T) {
	covered := make(map[string]bool)
	for _, sample := range eventSamples() {
		name, data, err := encodeEvent(sample)
		must(t, err)
		t.Run(name, func(t *testing.T) {
			decoded, err := decodeEvent(data)
			must(t, err)
			if !reflect.DeepEqual(decoded, sample) {
				t.Fatalf("round trip: got %#v, want %#v", decoded, sample)
			}
			covered[name] = true
		})
	}
	if len(covered) != len(eventTypes) {
		t.Fatalf("round-tripped %d variants, registry has %d", len(covered), len(eventTypes))
	}
}

func seedEvents() []queue.Event {
	return []queue.Event{
		queue.BaseMoved{SHA: "base"},
		queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.EnqueueRequested{PR: 1, At: epoch},
	}
}

func fold(state queue.State, events []queue.Event) queue.State {
	for _, event := range events {
		state = queue.Apply(state, event)
	}
	return state
}

func TestActionCodecRoundTripPreservesPrivateKeys(t *testing.T) {
	state := fold(queue.State{BaseBranch: "main", Fence: "fence"}, seedEvents())
	covered := make(map[string]bool)
	check := func(state queue.State, now time.Time) {
		t.Helper()
		for _, action := range queue.Plan(config, state, now) {
			name, data, err := encodeAction(action, config, now)
			must(t, err)
			decoded, err := decodeAction(data, action.Key(), state)
			must(t, err)
			if !reflect.DeepEqual(action, decoded) || action.Key() != decoded.Key() {
				t.Fatalf("%s round trip lost private fields: %#v / %#v", name, action, decoded)
			}
			covered[name] = true
		}
	}
	check(state, epoch)
	check(queue.Apply(state, queue.DequeueRequested{PR: 1}), epoch)
	state = queue.Apply(state, queue.TestRefBuilt{BatchID: "b1", TestedSHA: "tested", At: epoch, Method: queue.Squash})
	state = queue.Apply(state, queue.ChecksRefreshed{BatchID: "b1", TestedSHA: "tested", At: epoch})
	check(state, epoch.Add(2*time.Minute)) // RefreshChecks has a nonzero private epoch.
	state = queue.Apply(state, queue.ChecksCompleted{TestedSHA: "tested", Success: true})
	check(state, epoch)
	state = queue.Apply(state, queue.LandingStarted{BatchID: "b1", AttemptID: "attempt", FenceToken: "fence", At: epoch})
	check(state, epoch.Add(2*time.Minute))
	state = queue.Apply(state, queue.Landed{BatchID: "b1", NewBaseSHA: "tested", NewFence: "next"})
	check(state, epoch) // Includes admission/revision-scoped status and MarkLanded.
	for name := range actionTypes {
		if !covered[name] {
			t.Errorf("action %s lacks a round-trip sample", name)
		}
	}
}

func TestCodecRejectsUnknownOrInconsistentData(t *testing.T) {
	for _, data := range []string{`{`, `{"type":"future_event","data":{}}`, `{"type":"pr_closed","data":{"PR":"bad"}}`} {
		if _, err := decodeEvent([]byte(data)); err == nil {
			t.Fatalf("accepted invalid event %s", data)
		}
	}
	if _, _, err := encodeEvent(nil); err == nil {
		t.Fatal("accepted nil event")
	}
	state := fold(queue.State{BaseBranch: "main"}, seedEvents())
	action := queue.Plan(config, state, epoch)[0]
	_, data, err := encodeAction(action, config, epoch)
	must(t, err)
	var encoded envelope
	must(t, json.Unmarshal(data, &encoded))
	encoded.Data = json.RawMessage(`{"BatchID":"tampered"}`)
	corrupt, err := json.Marshal(encoded)
	must(t, err)
	for _, payload := range [][]byte{[]byte(`{`), []byte(`{"type":"future_action"}`), corrupt} {
		if _, err := decodeAction(payload, action.Key(), state); err == nil {
			t.Fatalf("accepted invalid action %s", payload)
		}
	}
}

func TestOnlyTransactionScopedAdvisoryLocks(t *testing.T) {
	files, err := filepath.Glob("*.go")
	must(t, err)
	sql, err := filepath.Glob("migrations/*.sql")
	must(t, err)
	files = append(files, sql...)
	sessionLock := regexp.MustCompile(`(?i)pg_(?:try_)?advisory_(?:lock|unlock)(?:_shared|_all)?\s*\(`)
	for _, file := range files {
		if regexp.MustCompile(`_test\.go$`).MatchString(file) {
			continue
		}
		data, err := os.ReadFile(file)
		must(t, err)
		if sessionLock.Match(data) {
			t.Errorf("session-level advisory lock in %s", file)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
