package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

var nonCommandBodies = []struct{ name, body, want string }{
	{"paragraph", "Please queue this:\n@openmerge queue", ""},
	{"backticks", "```sh\n@openmerge queue\n```", ""},
	{"tildes", "~~~example\n@openmerge queue\n~~~", ""},
	{"unclosed", "```\n@openmerge queue", ""},
	{"short close", "````\n```\n@openmerge queue\n````", ""},
	{"wrong close", "```\n~~~\n@openmerge queue", ""},
	{"close with text", "```\n``` nope\n@openmerge queue", ""},
	{"indented close", "```\n    ```\n@openmerge queue", ""},
	{"longer close", "```go\nexample\n````\n@openmerge refresh", ""},
	{"close whitespace", "   ~~~go\nexample\n ~~~~ \t\n@openmerge refresh", ""},
	{"invalid backtick info", "```go`\n@openmerge queue", ""},
	{"quoted", "> @openmerge queue", ""},
	{"lazy quote", "> Example command:\n@openmerge queue", ""},
	{"nested lazy quote", "  > > Example:\ncontinuation\n@openmerge queue", ""},
	{"quote blank", "> Example\n\n@openmerge queue", ""},
	{"quote heading", "> Example\n# New section\n@openmerge refresh", ""},
	{"quoted heading", "> # Heading\n@openmerge queue", ""},
	{"quoted fence", "> ```\n> example\n> ```\n@openmerge queue", ""},
	{"quoted list lazy", "> - Example command:\n@openmerge queue", ""},
	{"quoted ordered list lazy", "> 1. Example command:\n@openmerge queue", ""},
	{"empty list stays lazy", "> Example command:\n- \n@openmerge queue", ""},
	{"empty ordered list stays lazy", "> Example command:\n1. \n@openmerge queue", ""},
	{"quoted nested list lazy", "> - > Example command:\n@openmerge queue", ""},
	{"fence interrupts quote", "> example\n```\ncode\n```\n@openmerge queue", ""},
	{"list fence", "- ```\n  @openmerge queue\n  ```", ""},
	{"list fence tab", "- ```\n\tcode\n  @openmerge queue\n  ```", ""},
	{"list fence outdent", "- ```\n@openmerge queue", ""},
	{"quoted indented code", ">     example\n@openmerge queue", ""},
	{"real after example", "~~~\n@openmerge queue\n~~~\n@openmerge dequeue", ""},
	{"inline", "Use `@openmerge queue` please", ""},
	{"extra prose", "@openmerge queue please do it", ""},
	{"HTML comment", "<!--\n@openmerge queue\n-->", ""},
	{"HTML pre", "<pre>\n@openmerge queue\n</pre>", ""},
	{"nested list fence", "- item\n  - ```\n    @openmerge queue\n    ```", ""},
	{"two commands", "@openmerge queue\n@openmerge dequeue", ""},
	{"unknown command", "@openmerge pause", ""},
	{"mention case", "@OpenMerge queue", ""},
	{"command case", "@openmerge Queue", ""},
	{"double space", "@openmerge  queue", ""},
	{"double tab", "@openmerge\t\tqueue", ""},
	{"mixed separators", "@openmerge \tqueue", ""},
	{"double arg space", "@openmerge queue  abc1234", ""},
	{"non-ASCII separator", "@openmerge\u00a0queue", ""},
	{"non-ASCII arg", "@openmerge queue abc123é", ""},
	{"bare CR separator", "@openmerge\rqueue", ""},
	{"vertical tab separator", "@openmerge\vqueue", ""},
	{"form feed separator", "@openmerge\fqueue", ""},
	{"short SHA", "@openmerge queue abc123", ""},
	{"SHA prefix", "@openmerge queue abc1234", ""},
	{"39-hex SHA", "@openmerge queue " + strings.Repeat("a", 39), ""},
	{"leading NBSP", "\u00a0@openmerge queue", ""},
	{"trailing NBSP", "@openmerge queue\u00a0", ""},
	{"long SHA", "@openmerge queue " + strings.Repeat("a", 41), ""},
	{"nonhex SHA", "@openmerge queue not-a-sha", ""},
	{"dequeue args", "@openmerge dequeue abc1234", ""},
	{"refresh args", "@openmerge refresh please", ""},
}

func TestCommandGrammar(t *testing.T) {
	cases := append([]struct{ name, body, want string }{
		{"plain", "@openmerge queue", "queue"},
		{"surrounding whitespace", " \t\r\n    @openmerge dequeue \r\n", "dequeue"},
		{"tab", "\t@openmerge\trefresh\t", "refresh"},
		{"pinned tabs", "@openmerge\tqueue\t" + strings.Repeat("A", 40), "queue " + strings.Repeat("A", 40)},
		{"SHA-256", "@openmerge queue " + strings.Repeat("b", 64), "queue " + strings.Repeat("b", 64)},
		{"full SHA", "@openmerge queue " + strings.Repeat("a", 40), "queue " + strings.Repeat("a", 40)},
	}, nonCommandBodies...)
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := command(tt.body); got != tt.want {
				t.Fatalf("command(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestNonCommandsAreSilentlyIgnored(t *testing.T) {
	s, w, f, d := fixture(t, repositoryConfig)
	for _, tt := range nonCommandBodies {
		for _, author := range []string{"writer", "reader"} {
			f.AddComment(repo, 1, author, tt.body)
		}
	}
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if len(state.Entries) != 0 || state.Active != nil || len(f.Comments(repo, 1)) != 0 || d.permissions != 0 {
		t.Fatalf("non-command acted on: state=%+v replies=%v permissions=%d", state, f.Comments(repo, 1), d.permissions)
	}
}

func TestQueueCommandPinsHead(t *testing.T) {
	for _, mode := range []string{"stale unpinned", "stale full", "matching full", "matching uppercase", "first observation"} {
		t.Run(mode, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			if mode != "first observation" {
				must(t, s.reconcile(t.Context(), w, true))
			}
			body := "@openmerge queue"
			switch mode {
			case "matching uppercase":
				body += " " + strings.ToUpper(pr.HeadSHA)
			case "stale full", "matching full":
				body += " " + pr.HeadSHA
			}
			f.AddComment(repo, 1, "writer", body)
			if strings.HasPrefix(mode, "stale") {
				pr, err = f.PushPR(repo, 1, "new head")
				must(t, err)
			}
			// Restart before processing so binding uses the fresh head read.
			s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			w = s.queues[0]
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			accept := mode != "stale full"
			if (len(state.Entries) == 1) != accept {
				t.Fatalf("accept=%v: %+v", accept, state)
			}
			if accept && state.Entries[0].HeadSHA != pr.HeadSHA {
				t.Fatalf("queued head=%s, want current head=%s", state.Entries[0].HeadSHA, pr.HeadSHA)
			}
			replies := f.Comments(repo, 1)
			if len(replies) != 1 {
				t.Fatalf("expected one acceptance/refusal, got %v", replies)
			}
			for _, reply := range replies {
				if accept && !strings.Contains(reply, pr.HeadSHA) {
					t.Fatalf("acceptance omitted queued SHA: %s", reply)
				}
				if !accept && !strings.Contains(reply, "re-issue") {
					t.Fatalf("refusal omitted recovery command: %s", reply)
				}
			}
			must(t, s.cycle(t.Context(), w))
			if len(f.Comments(repo, 1)) != 1 {
				t.Fatal("command reply duplicated")
			}
			if accept {
				_, err = f.PushPR(repo, 1, "push after acceptance")
				must(t, err)
				must(t, s.cycle(t.Context(), w))
				state, _, err = s.store.Load(t.Context(), w.id)
				must(t, err)
				if len(state.Entries) != 0 {
					t.Fatalf("push after acceptance retained admission: %+v", state)
				}
			}
		})
	}
}

func TestUnpinnedCommandCannotApproveNewForkHead(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	base, err := f.ReadRef(t.Context(), repo, "main")
	must(t, err)
	pr, err := f.OpenPR(repo, 2, base, false)
	must(t, err)
	must(t, f.Approve(repo, 2, forge.Approval{User: "writer", SHA: pr.HeadSHA, CanWrite: true}))
	must(t, s.reconcile(t.Context(), w, true))
	f.AddComment(repo, 2, "writer", "@openmerge queue")
	pr, err = f.PushPR(repo, 2, "unapproved fork head before processing")
	must(t, err)
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	if len(state.Entries) != 0 || state.Active != nil {
		t.Fatalf("command bypassed exact-head fork trust: %+v", state)
	}
	replies := f.Comments(repo, 2)
	if len(replies) != 1 {
		t.Fatalf("expected one receipt naming current head, got %v", replies)
	}
	for _, reply := range replies {
		if !strings.Contains(reply, pr.HeadSHA) {
			t.Fatalf("receipt omitted current head: %s", reply)
		}
	}
	must(t, f.Approve(repo, 2, forge.Approval{User: "writer", SHA: pr.HeadSHA, CanWrite: true}))
	f.AddComment(repo, 2, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || len(state.Entries) != 1 || state.Entries[0].HeadSHA != pr.HeadSHA {
		t.Fatalf("writer approval did not authorize current fork head: %+v", state)
	}
}

func TestUnsafeMergeConditionsCannotLand(t *testing.T) {
	for _, tt := range []struct {
		name, rule string
		checks     []forge.CheckRun
	}{
		{"OR metadata bypass", "{or: [check-success = ci, base = main]}", nil},
		{"negated failure", "{not: check-success = ci}", []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Failure}}},
		{"zero count pending", "'#check-success >= 0'", []forge.CheckRun{{ID: 1, Name: "ci", State: forge.Pending}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, w, f, _ := fixture(t, "version: 1\nqueues:\n  - name: default\n    merge_when: ["+tt.rule+"]\n")
			base, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			f.SetCheckRuns(repo, pr.HeadSHA, tt.checks)
			built := false
			f.OnTestRef(func(sha string, _ []int) {
				built = true
				f.SetCheckRuns(repo, sha, tt.checks)
			})
			f.AddComment(repo, 1, "writer", "@openmerge queue")
			for range 3 {
				must(t, s.cycle(t.Context(), w))
			}
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			currentBase, err := f.ReadRef(t.Context(), repo, "main")
			must(t, err)
			pr, err = f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			if state.PauseReason(queue.Config{}) != "invalid-config" || state.Active != nil || built || currentBase != base || pr.Closed {
				t.Fatalf("unsafe condition allowed work: state=%+v built=%v base=%s closed=%v", state, built, currentBase, pr.Closed)
			}
			if w.configError == nil || !strings.Contains(w.configError.Error(), `queue "default"`) || !strings.Contains(w.configError.Error(), "merge_when: [check-success = ci]") {
				t.Fatalf("missing actionable refusal: %v", w.configError)
			}
		})
	}
}

func TestORCheckFailureWaitsForAlternative(t *testing.T) {
	s, w, f, _ := fixture(t, "version: 1\nqueues:\n  - name: default\n    merge_when:\n      - check-success = ci\n      - or: [check-success = linux, check-success = fallback]\n")
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	sha := state.Active.TestedSHA
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 4, Name: "ci", State: forge.Success}, {ID: 1, Name: "linux", State: forge.Failure}, {ID: 2, Name: "fallback", State: forge.Pending}})
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing || len(state.Entries) != 1 {
		t.Fatalf("OR failed before fallback completed: %+v", state)
	}
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 4, Name: "ci", State: forge.Success}, {ID: 1, Name: "linux", State: forge.Failure}, {ID: 3, Name: "fallback", State: forge.Success}})
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	if !pr.Closed {
		t.Fatal("successful OR alternative did not land and close PR")
	}
}

func TestDeferredBacklogDoesNotStarvePausedWork(t *testing.T) {
	s, w, f, _ := fixture(t, repositoryConfig)
	must(t, s.reconcile(t.Context(), w, true))
	pr, err := f.PullRequest(t.Context(), repo, 1)
	must(t, err)
	events := []queue.Event{queue.PRObserved{PR: 1, HeadSHA: pr.HeadSHA, Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()}}
	for n := 2; n <= 71; n++ {
		events = append(events, queue.PRObserved{PR: n, HeadSHA: "head", Eligible: false, Trusted: true}, queue.EnqueueRequested{PR: n, At: time.Now()})
	}
	_, err = s.store.Append(t.Context(), w.id, events, "")
	must(t, err)
	_, err = s.store.Plan(t.Context(), w.id, w.plannerConfig(time.Millisecond), time.Now())
	must(t, err)
	must(t, s.pause(t.Context(), w, "operator", "repairing base"))
	for range 2 {
		must(t, s.cycle(t.Context(), w))
	}
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	for _, action := range queue.Plan(w.plannerConfig(time.Millisecond), state, time.Now()) {
		if report, ok := action.(queue.ReportStatus); ok && report.Status == "paused" {
			t.Fatal("deferred backlog starved the pause report")
		}
	}
}
