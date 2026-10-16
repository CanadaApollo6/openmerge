package serve

import (
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCommandMarkdown(t *testing.T) {
	for _, tt := range []struct{ name, body, want string }{
		{"plain", "@openmerge queue", "queue"},
		{"three spaces", "   @openmerge dequeue \r", "dequeue"},
		{"paragraph", "Please queue this:\n@openmerge queue", "queue"},
		{"pinned", "@openmerge queue abc1234", "queue abc1234"},
		{"four spaces", "    @openmerge queue", ""},
		{"tab", "\t@openmerge queue", ""},
		{"space tab", " \t@openmerge queue", ""},
		{"backticks", "```sh\n@openmerge queue\n```", ""},
		{"tildes", "~~~example\n@openmerge queue\n~~~", ""},
		{"unclosed", "```\n@openmerge queue", ""},
		{"short close", "````\n```\n@openmerge queue\n````", ""},
		{"wrong close", "```\n~~~\n@openmerge queue", ""},
		{"close with text", "```\n``` nope\n@openmerge queue", ""},
		{"indented close", "```\n    ```\n@openmerge queue", ""},
		{"longer close", "```go\nexample\n````\n@openmerge refresh", "refresh"},
		{"close whitespace", "   ~~~go\nexample\n ~~~~ \t\n@openmerge refresh", "refresh"},
		{"invalid backtick info", "```go`\n@openmerge queue", "queue"},
		{"quoted", "> @openmerge queue", ""},
		{"lazy quote", "> Example command:\n@openmerge queue", ""},
		{"nested lazy quote", "  > > Example:\ncontinuation\n@openmerge queue", ""},
		{"quote blank", "> Example\n\n@openmerge queue", "queue"},
		{"quote heading", "> Example\n# New section\n@openmerge refresh", "refresh"},
		{"quoted heading", "> # Heading\n@openmerge queue", "queue"},
		{"quoted fence", "> ```\n> example\n> ```\n@openmerge queue", "queue"},
		{"quoted list lazy", "> - Example command:\n@openmerge queue", ""},
		{"quoted ordered list lazy", "> 1. Example command:\n@openmerge queue", ""},
		{"empty list stays lazy", "> Example command:\n- \n@openmerge queue", ""},
		{"empty ordered list stays lazy", "> Example command:\n1. \n@openmerge queue", ""},
		{"quoted nested list lazy", "> - > Example command:\n@openmerge queue", ""},
		{"fence interrupts quote", "> example\n```\ncode\n```\n@openmerge queue", "queue"},
		{"list fence", "- ```\n  @openmerge queue\n  ```", ""},
		{"list fence tab", "- ```\n\tcode\n  @openmerge queue\n  ```", ""},
		{"list fence outdent", "- ```\n@openmerge queue", "queue"},
		{"quoted indented code", ">     example\n@openmerge queue", "queue"},
		{"real after example", "~~~\n@openmerge queue\n~~~\n@openmerge dequeue", "dequeue"},
		{"inline", "Use `@openmerge queue` please", ""},
		{"extra prose", "@openmerge queue please do it", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := command(tt.body); got != tt.want {
				t.Fatalf("command(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestQueueCommandPinsHead(t *testing.T) {
	for _, mode := range []string{"stale unpinned", "stale prefix", "matching prefix", "matching full", "invalid prefix", "first observation"} {
		t.Run(mode, func(t *testing.T) {
			s, w, f, _ := fixture(t, repositoryConfig)
			pr, err := f.PullRequest(t.Context(), repo, 1)
			must(t, err)
			if mode != "first observation" {
				must(t, s.reconcile(t.Context(), w, true))
			}
			body := "@openmerge queue"
			switch mode {
			case "stale prefix", "matching prefix":
				body += " " + strings.ToUpper(pr.HeadSHA[:7])
			case "matching full":
				body += " " + pr.HeadSHA[:40]
			case "invalid prefix":
				body += " not-a-sha"
			}
			f.AddComment(repo, 1, "writer", body)
			if strings.HasPrefix(mode, "stale") {
				_, err = f.PushPR(repo, 1, "new unapproved head")
				must(t, err)
			}
			// Restart: the head evidence and receipts must survive the worker.
			s, err = New(t.Context(), s.store, []Target{w.Target}, s.options)
			must(t, err)
			w = s.queues[0]
			must(t, s.cycle(t.Context(), w))
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			accept := strings.HasPrefix(mode, "matching") || mode == "first observation"
			if (len(state.Entries) == 1) != accept {
				t.Fatalf("accept=%v: %+v", accept, state)
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
		})
	}
}

func TestORCheckFailureWaitsForAlternative(t *testing.T) {
	s, w, f, _ := fixture(t, "version: 1\nqueues:\n  - name: default\n    merge_when:\n      - or: [check-success = linux, check-success = fallback]\n")
	f.AddComment(repo, 1, "writer", "@openmerge queue")
	must(t, s.cycle(t.Context(), w))
	state, _, err := s.store.Load(t.Context(), w.id)
	must(t, err)
	sha := state.Active.TestedSHA
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "linux", State: forge.Failure}, {ID: 2, Name: "fallback", State: forge.Pending}})
	time.Sleep(2 * time.Millisecond)
	must(t, s.cycle(t.Context(), w))
	state, _, err = s.store.Load(t.Context(), w.id)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Testing || len(state.Entries) != 1 {
		t.Fatalf("OR failed before fallback completed: %+v", state)
	}
	f.SetCheckRuns(repo, sha, []forge.CheckRun{{ID: 1, Name: "linux", State: forge.Failure}, {ID: 3, Name: "fallback", State: forge.Success}})
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
