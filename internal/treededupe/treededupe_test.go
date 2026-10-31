package treededupe

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func at(minute int) time.Time { return t0.Add(time.Duration(minute) * time.Minute) }

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func inputs() Inputs {
	return Inputs{
		Namespace: "7\x00owner/repo\x00main\x00",
		Git: GitInputs{Tree: sha('a'), Workflows: []Workflow{
			{Mode: "100644", Type: "blob", OID: sha('b'), Path: ".github/workflows/ci.yml"},
			{Mode: "100644", Type: "blob", OID: sha('c'), Path: ".github/workflows/lint.yml"},
		}},
		Checks:      "and (check-success = ci)",
		RunnerImage: "ubuntu24/20261020.1",
	}
}

func TestKeyBindsEveryInputAndFailsClosed(t *testing.T) {
	base, unknown := Key(inputs())
	if base == "" || unknown != nil {
		t.Fatalf("known inputs: key=%q unknown=%v", base, unknown)
	}
	reordered := inputs()
	reordered.Git.Workflows[0], reordered.Git.Workflows[1] = reordered.Git.Workflows[1], reordered.Git.Workflows[0]
	if got, _ := Key(reordered); got != base {
		t.Fatal("workflow listing order changed the key")
	}
	changes := map[string]func(*Inputs){
		"namespace":     func(in *Inputs) { in.Namespace = "8\x00owner/repo\x00main\x00" },
		"tree":          func(in *Inputs) { in.Git.Tree = sha('d') },
		"workflow blob": func(in *Inputs) { in.Git.Workflows[0].OID = sha('d') },
		"workflow mode": func(in *Inputs) { in.Git.Workflows[0].Mode = "100755" },
		"workflow path": func(in *Inputs) { in.Git.Workflows[1].Path = ".github/workflows/lint2.yml" },
		"checks":        func(in *Inputs) { in.Checks = "and (check-success = ci; check-success = e2e)" },
		"runner image":  func(in *Inputs) { in.RunnerImage = "ubuntu24/20261027.1" },
	}
	for name, change := range changes {
		in := inputs()
		change(&in)
		if got, unknown := Key(in); got == "" || got == base || unknown != nil {
			t.Errorf("%s: key=%q unknown=%v", name, got, unknown)
		}
	}
	unknowns := map[string]struct {
		change func(*Inputs)
		want   UnknownInput
	}{
		"no namespace":       {func(in *Inputs) { in.Namespace = "" }, UnknownNamespace},
		"read error":         {func(in *Inputs) { in.GitErr = errors.New("busy") }, UnknownTree},
		"short tree":         {func(in *Inputs) { in.Git.Tree = "abc" }, UnknownTree},
		"uppercase tree":     {func(in *Inputs) { in.Git.Tree = strings.ToUpper(sha('a')) }, UnknownTree},
		"no workflows":       {func(in *Inputs) { in.Git.Workflows = nil }, UnknownWorkflows},
		"submodule workflow": {func(in *Inputs) { in.Git.Workflows[0].Type, in.Git.Workflows[0].Mode = "commit", "160000" }, UnknownWorkflows},
		"symlink workflow":   {func(in *Inputs) { in.Git.Workflows[0].Mode = "120000" }, UnknownWorkflows},
		"outside workflows":  {func(in *Inputs) { in.Git.Workflows[0].Path = "ci.yml" }, UnknownWorkflows},
		"duplicate path":     {func(in *Inputs) { in.Git.Workflows[1].Path = in.Git.Workflows[0].Path }, UnknownWorkflows},
		"no checks":          {func(in *Inputs) { in.Checks = " " }, UnknownChecks},
		"no runner image":    {func(in *Inputs) { in.RunnerImage = "" }, UnknownRunnerImage},
		"mutable image tag":  {func(in *Inputs) { in.RunnerImage = "ubuntu-latest" }, UnknownRunnerImage},
		"spaced image":       {func(in *Inputs) { in.RunnerImage = "ubuntu 24" }, UnknownRunnerImage},
	}
	for name, tc := range unknowns {
		in := inputs()
		tc.change(&in)
		got, unknown := Key(in)
		if got != "" || len(unknown) == 0 || !contains(unknown, tc.want) {
			t.Errorf("%s: key=%q unknown=%v, want %s", name, got, unknown, tc.want)
		}
	}
}

func contains(list []UnknownInput, want UnknownInput) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func build(batch string, c byte, minute int, key string) Build {
	return Build{At: at(minute), BatchID: batch, SHA: sha(c), Kind: KindBatch, Key: key}
}

func summary(l Ledger) Summary { return l.Summarize(time.Time{}, time.Time{}) }

func TestLedgerReusesOnlyTrustedSuccessesAndConfirmedFailures(t *testing.T) {
	t.Run("same tree rebuilt after invalidation reuses a completed success", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveOutcome(sha('1'), at(12), true)
		l.ObserveBuild(build("b2", '2', 13, "k"))
		l.ObserveOutcome(sha('2'), at(25), true)
		s := summary(l)
		if s.Builds != 2 || s.Keyed != 2 || s.WouldReuseSuccess != 1 || s.AvoidableMinutes != 12 || s.Agreed != 1 || s.HitsByKind[KindBatch] != 1 {
			t.Fatalf("summary = %+v", s)
		}
		if b := l.Builds[1]; b.SourceBatchID != "b1" || b.SourceSHA != sha('1') {
			t.Fatalf("hit source = %+v", b)
		}
	})
	t.Run("in-flight source is not reusable", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveBuild(build("b2", '2', 1, "k"))
		l.ObserveOutcome(sha('1'), at(12), true)
		if s := summary(l); s.WouldReuseSuccess != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("different key misses", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k1"))
		l.ObserveOutcome(sha('1'), at(12), true)
		l.ObserveBuild(build("b2", '2', 13, "k2"))
		if s := summary(l); s.WouldReuseSuccess != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("unknown key never hits even beside a known success", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(Build{At: at(0), BatchID: "b1", SHA: sha('1'), Kind: KindBatch, Unknown: []string{"runner_image"}})
		l.ObserveOutcome(sha('1'), at(12), true)
		l.ObserveBuild(Build{At: at(13), BatchID: "b2", SHA: sha('2'), Kind: KindBatch, Unknown: []string{"runner_image"}})
		if s := summary(l); s.WouldReuseSuccess != 0 || s.KeyUnknown["runner_image"] != 2 || s.Keyed != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("unconfirmed failure is never a candidate and shadows older results", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b0", '0', 0, "k"))
		l.ObserveOutcome(sha('0'), at(5), true)
		b := build("b1", '1', 6, "k")
		b.RetryConfigured = true
		l.ObserveBuild(b)
		l.ObserveOutcome(sha('1'), at(12), false)
		l.ObserveBuild(build("b2", '2', 13, "k"))
		s := summary(l)
		if s.WouldReuseFailure != 0 || s.WouldReuseSuccess != 1 {
			// b1 reused b0's success (and later failed, unconfirmed); b2 sees
			// the newest result, an unconfirmed failure, and must miss.
			t.Fatalf("summary = %+v", s)
		}
		if l.Builds[2].Hit != "" {
			t.Fatalf("b2 hit on an unconfirmed failure: %+v", l.Builds[2])
		}
	})
	t.Run("multi-PR failure without retry is unconfirmed", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveOutcome(sha('1'), at(12), false)
		l.ObserveBuild(build("b2", '2', 13, "k"))
		if s := summary(l); s.WouldReuseFailure != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("retry-confirmed failure hits", func(t *testing.T) {
		var l Ledger
		b := build("b1", '1', 0, "k")
		b.RetryConfigured = true
		l.ObserveBuild(b)
		l.ObserveOutcome(sha('1'), at(12), false)
		l.ObserveConfirmedFailure("b1", sha('1'), at(30))
		right := build("b3", '3', 31, "k")
		right.Kind, right.Singleton, right.RetryConfigured = KindBisection, true, true
		l.ObserveBuild(right)
		l.ObserveOutcome(sha('3'), at(40), false)
		l.ObserveConfirmedFailure("b3", sha('3'), at(55))
		s := summary(l)
		if s.WouldReuseFailure != 1 || s.AvoidableMinutes != 12 || s.HitsByKind[KindBisection] != 1 || s.Agreed != 1 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("singleton failure without retry is confirmed", func(t *testing.T) {
		var l Ledger
		b := build("b1", '1', 0, "k")
		b.Singleton = true
		l.ObserveBuild(b)
		l.ObserveOutcome(sha('1'), at(12), false)
		l.ObserveBuild(build("b2", '2', 13, "k"))
		if s := summary(l); s.WouldReuseFailure != 1 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("singleton failure awaiting its retry is unconfirmed", func(t *testing.T) {
		var l Ledger
		b := build("b1", '1', 0, "k")
		b.Singleton, b.RetryConfigured = true, true
		l.ObserveBuild(b)
		l.ObserveOutcome(sha('1'), at(12), false)
		l.ObserveBuild(build("b2", '2', 13, "k"))
		if s := summary(l); s.WouldReuseFailure != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("confirmation must name the batch", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveOutcome(sha('1'), at(12), false)
		l.ObserveConfirmedFailure("other", sha('1'), at(30))
		l.ObserveBuild(build("b2", '2', 31, "k"))
		if s := summary(l); s.WouldReuseFailure != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("reopened success is revoked", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveOutcome(sha('1'), at(12), true)
		l.ObserveReopened(sha('1'))
		l.ObserveBuild(build("b2", '2', 13, "k"))
		if s := summary(l); s.WouldReuseSuccess != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("flaky disagreement is recorded", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveOutcome(sha('1'), at(12), true)
		l.ObserveBuild(build("b2", '2', 13, "k"))
		l.ObserveOutcome(sha('2'), at(25), false) // no retry: the queue acts on it
		if s := summary(l); s.WouldReuseSuccess != 1 || s.Disagreed != 1 || s.Agreed != 0 {
			t.Fatalf("summary = %+v", s)
		}
	})
	t.Run("duplicate observation is idempotent", func(t *testing.T) {
		var l Ledger
		l.ObserveBuild(build("b1", '1', 0, "k"))
		l.ObserveBuild(build("b1", '1', 1, "k"))
		if len(l.Builds) != 1 {
			t.Fatalf("builds = %d", len(l.Builds))
		}
	})
}

func TestLedgerBoundWindowAndCodec(t *testing.T) {
	var l Ledger
	for i := range MaxBuilds + 3 {
		l.ObserveBuild(Build{At: t0.Add(time.Duration(i) * time.Second), BatchID: "b", SHA: fmt.Sprintf("%040d", i), Kind: KindBatch})
	}
	if len(l.Builds) != MaxBuilds || l.Dropped != 3 || !l.Builds[0].At.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("bound: len=%d dropped=%d first=%v", len(l.Builds), l.Dropped, l.Builds[0].At)
	}
	s := l.Summarize(t0.Add(10*time.Second), t0.Add(20*time.Second))
	if s.Builds != 10 || s.Retained != MaxBuilds || s.Dropped != 3 {
		t.Fatalf("window summary = %+v", s)
	}
	raw, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(raw)
	if err != nil || len(back.Builds) != MaxBuilds || back.Dropped != 3 {
		t.Fatalf("round trip: %v", err)
	}
	if fresh, err := Decode(nil); err != nil || fresh.Version != LedgerVersion {
		t.Fatalf("empty decode: %+v %v", fresh, err)
	}
	if _, err := Decode([]byte(`{"version":2,"builds":[]}`)); !errors.Is(err, ErrLedgerVersion) {
		t.Fatalf("future version accepted: %v", err)
	}
	if _, err := Decode([]byte(`{`)); err == nil {
		t.Fatal("corrupt ledger accepted")
	}
}
