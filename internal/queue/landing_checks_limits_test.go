package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

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

func landingChecksAtByteCap(t *testing.T) LandingChecksRecorded {
	t.Helper()
	e := checkFixture(t)
	for {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		gap := MaxLandingChecksBytes - landingChecksEnvelopeBytes - len(raw)
		if gap <= 60000 {
			e.Expressions[0] += strings.Repeat("x", gap)
			return e
		}
		index := len(e.Checks)
		e.Checks = append(e.Checks, LandingCheck{Name: fmt.Sprintf("%04d%s", index, strings.Repeat("x", 4092)), ProducerKind: "unavailable", Result: forge.Success})
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
