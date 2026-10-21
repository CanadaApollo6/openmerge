package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestLandingChecksDeduplicatesExactRead(t *testing.T) {
	header := checkFixture(t)
	b, err := NewLandingChecksBuilder(header)
	if err != nil {
		t.Fatal(err)
	}
	run := header.Checks[0]
	for _, c := range []LandingCheck{run, header.Checks[1], run} {
		if err := b.AddCheck(c); err != nil {
			t.Fatal(err)
		}
	}
	expr, err := condition.Parse("check-success = ci")
	if err != nil {
		t.Fatal(err)
	}
	values := condition.CheckResults([]string{"ci", "lint"}, nil, nil)
	want := expr.Record(values)
	for pr := 1; pr <= 128; pr++ {
		if err := b.RecordMember(pr, strings.Repeat("c", 40), expr, values); err != nil {
			t.Fatal(err)
		}
	}
	e := b.Event()
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(e.Checks) != 2 || !reflect.DeepEqual(e.CheckOrder, []int{0, 1, 0}) || len(e.Expressions) != 1 || len(e.Inputs) != 1 || len(e.Members) != 128 {
		t.Fatalf("evidence expanded: %+v", e)
	}
	got := conditionForMember(t, e, 127)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved input differs: %+v", got)
	}
	*run.ProducerIdentity = "changed"
	values["check-success"] = condition.Strings("changed")
	got = conditionForMember(t, e, 0)
	if !reflect.DeepEqual(got, want) || *e.Checks[0].ProducerIdentity == "changed" {
		t.Fatal("evidence shares caller memory")
	}
}

func TestLandingChecksRejectsAstra128By1000(t *testing.T) {
	b, err := NewLandingChecksBuilder(checkFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	// 1,000 1-KiB names fit the old 4,096-byte per-name cap. Repeating the
	// success/observed input for 128 members made the old event ~258 MB.
	names := make([]string, 1000)
	for i := range names {
		names[i] = fmt.Sprintf("%04d%s", i, strings.Repeat("x", 996))
	}
	values := condition.CheckResults(names, nil, nil)
	expr, err := condition.Parse("#check-success >= 1")
	if err != nil {
		t.Fatal(err)
	}
	for pr := 1; pr <= 128; pr++ {
		err = b.RecordMember(pr, strings.Repeat("c", 40), expr, values)
		if err != nil {
			break
		}
	}
	var tooLarge LandingChecksTooLargeError
	if !errors.Is(err, ErrLandingChecksTooLarge) || !errors.As(err, &tooLarge) {
		t.Fatalf("large evidence accepted or wrong error: %v", err)
	}
	if len(b.event.Inputs) != 0 || len(b.event.Members) != 0 {
		t.Fatal("oversized input expanded before budget check")
	}
	// The run table independently fails closed, even without referenced inputs.
	b, err = NewLandingChecksBuilder(checkFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		err = b.AddCheck(LandingCheck{Name: name, ProducerKind: "unavailable", Result: forge.Success})
		if err != nil {
			break
		}
	}
	if !errors.Is(err, ErrLandingChecksTooLarge) {
		t.Fatal("oversized check table accepted", err)
	}
}

// Track only the appended JSON array elements while constructing the fixture.
// The final boundary is still checked against the real encoder by the test.
func landingChecksAtByteCap(t *testing.T) LandingChecksRecorded {
	t.Helper()
	e := checkFixture(t)
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	size := len(raw)
	for {
		gap := MaxLandingChecksBytes - landingChecksEnvelopeBytes - size
		if gap <= 60000 {
			e.Expressions[0] += strings.Repeat("x", gap)
			return e
		}
		index := len(e.Checks)
		check := LandingCheck{Name: fmt.Sprintf("%04d%s", index, strings.Repeat("x", 4092)), ProducerKind: "unavailable", Result: forge.Success}
		raw, err := json.Marshal(check)
		if err != nil {
			t.Fatal(err)
		}
		size += len(raw) + len(strconv.Itoa(index))
		if len(e.Checks) > 0 {
			size++ // checks comma
		}
		if len(e.CheckOrder) > 0 {
			size++ // check_order comma
		}
		e.Checks = append(e.Checks, check)
		e.CheckOrder = append(e.CheckOrder, index)
	}
}

func TestLandingChecksExactByteBoundary(t *testing.T) {
	e := landingChecksAtByteCap(t)
	_, raw, err := EncodeEvent(e)
	if err != nil || len(raw) != MaxLandingChecksBytes {
		t.Fatalf("cap boundary: bytes=%d err=%v", len(raw), err)
	}
	if _, err := DecodeEvent(raw); err != nil {
		t.Fatal("cap event cannot replay", err)
	}
	b, err := NewLandingChecksBuilder(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range e.Checks {
		if err := b.AddCheck(c); err != nil {
			t.Fatal(err)
		}
	}
	// The cumulative builder must agree with encoding/json including table
	// commas, decimal indexes, and envelope overhead.
	size, err := landingJSONBytes(reflect.ValueOf(b.event), MaxLandingChecksBytes)
	if err != nil || b.bytes != size+landingChecksEnvelopeBytes {
		t.Fatalf("builder budget drift: %d vs %d: %v", b.bytes, size+landingChecksEnvelopeBytes, err)
	}
	e.Expressions[0] += "x"
	if _, _, err := EncodeEvent(e); !errors.Is(err, ErrLandingChecksTooLarge) {
		t.Fatal("cap+1 serialized", err)
	}
	raw, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readLandingChecksWire(raw); !errors.Is(err, ErrLandingChecksTooLarge) {
		t.Fatal("cap+1 replay decoded", err)
	}
}

func TestLandingChecksJSONSizeMatchesEncoder(t *testing.T) {
	for _, text := range []string{"", "ascii", "\"\\\b\f\n\r\t\x00<>&", "世界\u2028\u2029"} {
		e := checkFixture(t)
		e.Checks[0].Name = text
		e.ObservedAt = e.ObservedAt.In(time.FixedZone("test", -7*3600))
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		size, err := landingJSONBytes(reflect.ValueOf(e), MaxLandingChecksBytes)
		if err != nil || size != len(raw) {
			t.Fatalf("size mismatch for %q: %d vs %d: %v", text, size, len(raw), err)
		}
	}
}

func TestLandingChecksStringAndReferenceLimits(t *testing.T) {
	for name, mutate := range map[string]func(*LandingChecksRecorded){
		"check-name":      func(e *LandingChecksRecorded) { e.Checks[0].Name = strings.Repeat("x", 4097) },
		"source":          func(e *LandingChecksRecorded) { e.Checks[0].Source = strings.Repeat("x", 4097) },
		"identity":        func(e *LandingChecksRecorded) { s := strings.Repeat("x", 4097); e.Checks[0].ProducerIdentity = &s },
		"branch":          func(e *LandingChecksRecorded) { s := strings.Repeat("x", 4097); e.Checks[0].HeadBranch = &s },
		"expression":      func(e *LandingChecksRecorded) { e.Expressions[0] = strings.Repeat("x", 65537) },
		"input-text":      func(e *LandingChecksRecorded) { e.Inputs[0].Text = strings.Repeat("x", 4097) },
		"input-list":      func(e *LandingChecksRecorded) { e.Inputs[0].List = []string{strings.Repeat("x", 4097)} },
		"input-observed":  func(e *LandingChecksRecorded) { e.Inputs[0].Observed = []string{strings.Repeat("x", 4097)} },
		"input-unsettled": func(e *LandingChecksRecorded) { e.Inputs[0].Unsettled = []string{strings.Repeat("x", 4097)} },
		"event-identity":  func(e *LandingChecksRecorded) { e.BatchID = strings.Repeat("x", 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			e := checkFixture(t)
			mutate(&e)
			if err := e.Validate(); !errors.Is(err, ErrLandingChecksTooLarge) {
				t.Fatal("missing typed string limit", err)
			}
		})
	}
	for name, mutate := range map[string]func(*LandingChecksRecorded){
		"invalid-utf8":       func(e *LandingChecksRecorded) { e.Checks[0].Name = string([]byte{0xff}) },
		"negative-check":     func(e *LandingChecksRecorded) { e.CheckOrder[0] = -1 },
		"missing-check":      func(e *LandingChecksRecorded) { e.CheckOrder[0] = len(e.Checks) },
		"missing-expression": func(e *LandingChecksRecorded) { e.Members[0].Conditions.Expression = len(e.Expressions) },
		"missing-input":      func(e *LandingChecksRecorded) { e.Members[0].Conditions.Inputs[0] = len(e.Inputs) },
		"unordered-input":    func(e *LandingChecksRecorded) { e.Members[0].Conditions.Inputs = []int{1, 0} },
		"duplicate-input":    func(e *LandingChecksRecorded) { e.Inputs = append(e.Inputs, e.Inputs[0]) },
		"duplicate-check": func(e *LandingChecksRecorded) {
			e.Checks = append(e.Checks, e.Checks[0])
			e.CheckOrder = append(e.CheckOrder, len(e.Checks)-1)
		},
		"duplicate-expression": func(e *LandingChecksRecorded) { e.Expressions = append(e.Expressions, e.Expressions[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			e := checkFixture(t)
			mutate(&e)
			if err := e.Validate(); err == nil {
				t.Fatal("bad shared table accepted")
			}
		})
	}
}

func conditionForMember(t *testing.T, e LandingChecksRecorded, member int) condition.Evidence {
	t.Helper()
	refs := e.Members[member].Conditions
	out := condition.Evidence{Expression: e.Expressions[refs.Expression], Inputs: []condition.ObservedInput{}}
	for _, index := range refs.Inputs {
		out.Inputs = append(out.Inputs, e.Inputs[index].Clone())
	}
	return out
}

func TestLandingChecksSharesInputsAcrossDifferentMembers(t *testing.T) {
	b, err := NewLandingChecksBuilder(checkFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	expr, err := condition.Parse("check-success = ci")
	if err != nil {
		t.Fatal(err)
	}
	review, err := condition.Parse("#approved-reviews-by >= 1")
	if err != nil {
		t.Fatal(err)
	}
	expr = condition.All(expr, review)
	for pr := 1; pr <= 2; pr++ {
		values := condition.CheckResults([]string{"ci"}, nil, nil)
		values["approved-reviews-by"] = condition.Strings(fmt.Sprintf("reviewer-%d", pr))
		if err := b.RecordMember(pr, strings.Repeat("c", 40), expr, values); err != nil {
			t.Fatal(err)
		}
	}
	e := b.Event()
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(e.Expressions) != 1 || len(e.Inputs) != 3 || e.Members[0].Conditions.Inputs[1] != e.Members[1].Conditions.Inputs[1] || e.Members[0].Conditions.Inputs[0] == e.Members[1].Conditions.Inputs[0] {
		t.Fatal("shared CI or distinct review evidence lost")
	}
	size, err := landingJSONBytes(reflect.ValueOf(e), MaxLandingChecksBytes)
	if err != nil || b.bytes != size+landingChecksEnvelopeBytes {
		t.Fatal("member budget drift", err)
	}
}

func TestLandingChecksBuilderRejectsInvalidEvidence(t *testing.T) {
	for name, build := range map[string]func() error{
		"missing-attempt": func() error {
			header := checkFixture(t)
			header.AttemptID = ""
			_, err := NewLandingChecksBuilder(header)
			return err
		},
		"invalid-time": func() error {
			header := checkFixture(t)
			header.ObservedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			_, err := NewLandingChecksBuilder(header)
			return err
		},
		"invalid-utf8": func() error {
			b, err := NewLandingChecksBuilder(checkFixture(t))
			if err != nil {
				return err
			}
			return b.AddCheck(LandingCheck{Name: string([]byte{0xff}), ProducerKind: "unavailable", Result: forge.Success})
		},
		"fabricated-id": func() error {
			b, err := NewLandingChecksBuilder(checkFixture(t))
			if err != nil {
				return err
			}
			zero := uint64(0)
			return b.AddCheck(LandingCheck{Name: "ci", ProducerKind: "unavailable", Result: forge.Success, RunID: &zero})
		},
		"invalid-member": func() error {
			b, err := NewLandingChecksBuilder(checkFixture(t))
			if err != nil {
				return err
			}
			return b.RecordMember(0, strings.Repeat("c", 40), condition.Expression{}, condition.Snapshot{})
		},
		"invalid-input": func() error {
			b, err := NewLandingChecksBuilder(checkFixture(t))
			if err != nil {
				return err
			}
			expr, err := condition.Parse("title = ready")
			if err != nil {
				return err
			}
			return b.RecordMember(1, strings.Repeat("c", 40), expr, condition.Snapshot{"title": condition.Bool(true)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := build()
			var invalid LandingChecksInvalidError
			if !errors.Is(err, ErrLandingChecksInvalid) || !errors.As(err, &invalid) || errors.Is(err, ErrLandingChecksTooLarge) {
				t.Fatalf("invalid evidence lost permanent classification: %v", err)
			}
		})
	}
}

func TestLandingChecksDirectCollectionCaps(t *testing.T) {
	for name, mutate := range map[string]func(*LandingChecksRecorded){
		"members":     func(e *LandingChecksRecorded) { e.Members = make([]LandingCheckMember, maxLandingCheckMembers+1) },
		"checks":      func(e *LandingChecksRecorded) { e.Checks = make([]LandingCheck, maxLandingCheckRuns+1) },
		"check-order": func(e *LandingChecksRecorded) { e.CheckOrder = make([]int, maxLandingCheckRuns+1) },
		"expressions": func(e *LandingChecksRecorded) { e.Expressions = make([]string, maxLandingCheckMembers+1) },
		"inputs":      func(e *LandingChecksRecorded) { e.Inputs = make([]condition.ObservedInput, maxLandingCheckInputs+1) },
		"source-revisions": func(e *LandingChecksRecorded) {
			e.RequiredCheckSource.SourceRevisions = make([]LandingCheckSourceRevision, len(rulesSourceOrder)+1)
		},
		"member-inputs": func(e *LandingChecksRecorded) { e.Members[0].Conditions.Inputs = make([]int, 13) },
		"member-head": func(e *LandingChecksRecorded) {
			e.Members[0].HeadSHA = strings.Repeat("x", MaxLandingCheckStringBytes+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := checkFixture(t)
			mutate(&e)
			err := e.Validate()
			var limit LandingChecksTooLargeError
			if !errors.As(err, &limit) || limit.Field != strings.ReplaceAll(name, "-", " ") {
				t.Fatalf("missing direct cap for %s: %v", name, err)
			}
		})
	}
	for name, mutate := range map[string]func(*LandingChecksRecorded){
		"identity": func(e *LandingChecksRecorded) { e.AttemptID = strings.Repeat("x", MaxLandingCheckStringBytes+1) },
		"source-revisions": func(e *LandingChecksRecorded) {
			e.RequiredCheckSource.SourceRevisions = make([]LandingCheckSourceRevision, len(rulesSourceOrder)+1)
		},
	} {
		t.Run("header-"+name, func(t *testing.T) {
			e := checkFixture(t)
			mutate(&e)
			if _, err := NewLandingChecksBuilder(e); !errors.Is(err, ErrLandingChecksTooLarge) {
				t.Fatalf("missing direct header cap: %v", err)
			}
		})
	}
}

func TestLandingChecksBuilderBudgetsFinalAttempt(t *testing.T) {
	header := checkFixture(t)
	// Escapes cost two wire bytes; this must be the exact final attempt, not
	// an empty placeholder or an estimate based on unescaped string length.
	header.AttemptID = strings.Repeat("\"", MaxLandingCheckStringBytes)
	b, err := NewLandingChecksBuilder(header)
	if err != nil {
		t.Fatal(err)
	}
	expr, err := condition.Parse("check-success = ci")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RecordMember(1, strings.Repeat("c", 40), expr, condition.CheckResults([]string{"ci"}, nil, nil)); err != nil {
		t.Fatal(err)
	}
	for {
		c := LandingCheck{Name: fmt.Sprintf("padding-%d-", len(b.event.Checks)), ProducerKind: "unavailable", Result: forge.Success}
		e := b.Event()
		e.Checks = append(slices.Clone(e.Checks), c)
		e.CheckOrder = append(slices.Clone(e.CheckOrder), len(e.Checks)-1)
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		gap := MaxLandingChecksBytes - landingChecksEnvelopeBytes - len(raw)
		if gap < 0 {
			t.Fatal("padding overshot boundary")
		}
		padding := min(gap, MaxLandingCheckStringBytes-len(c.Name))
		c.Name += strings.Repeat("x", padding)
		if err := b.AddCheck(c); err != nil {
			t.Fatal(err)
		}
		if padding == gap {
			break
		}
	}
	e := b.Event()
	_, raw, err := EncodeEvent(e)
	if err != nil || len(raw) != MaxLandingChecksBytes || b.bytes != len(raw) {
		t.Fatalf("final-attempt boundary: %d/%d: %v", b.bytes, len(raw), err)
	}
	if err := b.AddCheck(LandingCheck{Name: "overflow", ProducerKind: "unavailable", Result: forge.Success}); !errors.Is(err, ErrLandingChecksTooLarge) {
		t.Fatalf("cap+1 accepted: %v", err)
	}
	if _, err := DecodeEvent(raw); err != nil {
		t.Fatal(err)
	}
}

func FuzzLandingChecksJSONSizeMatchesEncoder(f *testing.F) {
	for _, seed := range []string{"", "ascii", "\"\\\b\f\n\r\t\x00<>&", "世界\u2028\u2029"} {
		f.Add(seed, uint64(0), int64(0), false)
	}
	f.Add("optional nulls", ^uint64(0), int64(-1<<63), true)
	f.Add("numeric boundaries", ^uint64(0), int64(-1<<63), false)
	f.Fuzz(func(t *testing.T, text string, id uint64, index int64, nullable bool) {
		if len(text) > 4096 || !utf8.ValidString(text) {
			t.Skip()
		}
		e := LandingChecksRecorded{BatchID: text, AttemptID: text, ObservedAt: time.Date(2026, 10, 21, 1, 2, 3, int(id%1e9), time.FixedZone("test", int(index%86400)))}
		if !nullable {
			e.Members = []LandingCheckMember{{PR: int(index), HeadSHA: text, Conditions: LandingCheckCondition{Expression: int(index), Inputs: []int{int(index), 0}}}}
			e.Checks = []LandingCheck{{Name: text, Source: text, ProducerIdentity: &text, RunID: &id, Attempt: &id, WorkflowRunID: &id, HeadBranch: &text}}
			e.CheckOrder, e.Expressions = []int{int(index)}, []string{text}
			e.Inputs = []condition.ObservedInput{{Attribute: text, Kind: text, Text: text, List: []string{text}, Flag: id%2 == 0, Checks: id%2 == 1, Observed: []string{text}, Unsettled: []string{text}}}
		}
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		size, err := landingJSONBytes(reflect.ValueOf(e), len(raw))
		if err != nil || size != len(raw) {
			t.Fatalf("oracle mismatch: %d vs %d: %v", size, len(raw), err)
		}
		if _, err := landingJSONBytes(reflect.ValueOf(e), len(raw)-1); !errors.Is(err, ErrLandingChecksTooLarge) {
			t.Fatalf("oracle under-budget accepted: %v", err)
		}
	})
}
