package changepolicy

import (
	"encoding/json"
	"testing"
)

func digestInputs(t *testing.T) DigestInputs {
	t.Helper()
	r := fixture(t)
	return DigestInputs{Repository: r, Queue: r.Queues[0], ClassifierVersion: "classifier-v1",
		Resolved:   []ResolvedIdentity{{Name: "acme-coder[bot]", ID: "bot", Kind: AgentAccount}, {Name: "alice", ID: "a", Kind: HumanAccount}, {Name: "bob", ID: "b", Kind: HumanAccount}, {Name: "acme/platform", ID: "team"}},
		Owners:     GoverningContent{Path: ".github/CODEOWNERS", Known: true, Present: true, Content: "* @acme/platform\n"},
		Attributes: []GoverningContent{{Path: ".gitattributes", Known: true}},
	}
}

func TestSemanticPolicyDigest(t *testing.T) {
	in := digestInputs(t)
	want, err := PolicyDigest(in)
	if err != nil {
		t.Fatal(err)
	}
	const vector = "6fd2a7569b7259c86a11209759ea489ade2113ea715e98ded2ca9791b6c2f4e6"
	if want != vector {
		t.Fatalf("digest vector = %s", want)
	}
	for _, edit := range []struct {
		name   string
		change func(*DigestInputs)
		same   bool
	}{
		{"other-queue", func(in *DigestInputs) {
			in.Repository.Queues[1].Approvals = &Approvals{Default: Requirement{Selector: Human, Count: 9}}
		}, true},
		{"set-and-rule-order", func(in *DigestInputs) {
			in.Repository.Classification.Docs = []string{"**/*.md", "docs/**", "docs/**"}
			in.Repository.Groups["platform"] = []string{"alice", "acme/platform", "alice"}
			in.Repository.Agents[0].Accountable = []string{"bob", "alice", "bob"}
			in.Resolved[0], in.Resolved[1] = in.Resolved[1], in.Resolved[0]
			in.Queue.Approvals.Rules[0], in.Queue.Approvals.Rules[1] = in.Queue.Approvals.Rules[1], in.Queue.Approvals.Rules[0]
		}, true},
		{"classifier-version-only-refresh", func(in *DigestInputs) { in.ClassifierVersion = "classifier-v2" }, false},
		{"login-reassignment", func(in *DigestInputs) { in.Resolved[1].ID = "new-user" }, false},
		{"team-reassignment", func(in *DigestInputs) { in.Resolved[3].ID = "new-team" }, false},
		{"classification", func(in *DigestInputs) {
			in.Repository.Classification.Sensitive = append(in.Repository.Classification.Sensitive, "private/**")
		}, false},
		{"default", func(in *DigestInputs) { in.Queue.Approvals.Default = Requirement{Selector: Human, Count: 1} }, false},
		{"count", func(in *DigestInputs) { in.Queue.Approvals.Rules[1].Require.Count++ }, false},
		{"receipt", func(in *DigestInputs) { in.Queue.Approvals.Rules[1].Require.Receipt = true }, false},
		{"accountability", func(in *DigestInputs) { in.Repository.Agents[0].Accountable = []string{"bob"} }, false},
		{"owners-content", func(in *DigestInputs) { in.Owners.Content = "* @alice\n" }, false},
		{"attributes-content", func(in *DigestInputs) {
			in.Attributes[0].Present = true
			in.Attributes[0].Content = "gen/** linguist-generated\n"
		}, false},
	} {
		t.Run(edit.name, func(t *testing.T) {
			candidate := digestInputs(t)
			edit.change(&candidate)
			before, _ := json.Marshal(candidate)
			got, err := PolicyDigest(candidate)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(candidate)
			if (got == want) != edit.same {
				t.Fatalf("same=%v got=%s want=%s", edit.same, got, want)
			}
			if string(before) != string(after) {
				t.Fatal("canonicalization mutated input")
			}
		})
	}
	for _, edit := range []func(*DigestInputs){
		func(in *DigestInputs) { in.Owners.Known = false },
		func(in *DigestInputs) { in.Attributes = append(in.Attributes, in.Attributes[0]) },
		func(in *DigestInputs) { in.Resolved[1].ID = "" },
		func(in *DigestInputs) { in.Resolved = append(in.Resolved, in.Resolved[0]) },
		func(in *DigestInputs) { in.ClassifierVersion = "" },
	} {
		candidate := digestInputs(t)
		edit(&candidate)
		if _, err := PolicyDigest(candidate); err == nil {
			t.Fatal("unresolved policy got authoritative digest")
		}
	}
}

func TestCandidateFormattingDigest(t *testing.T) {
	in := digestInputs(t)
	base := ""
	for _, source := range []string{
		"# provenance-only formatting\nqueues: [{name: main, approvals: {default: any_writer, rules: [{when: {labels: [docs]}, require: {human: true}}]}}]\n",
		"queues:\n- name: main\n  approvals:\n    rules:\n    - require: {count: 1, any_writer: true, human: true}\n      when: {labels: [docs, docs]}\n    default: {any_writer: true, count: 1}\n",
	} {
		r, _, err := ParseCandidate([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		in.Repository = r
		in.Queue = r.Queues[0]
		in.Resolved = nil
		got, err := PolicyDigest(in)
		if err != nil {
			t.Fatal(err)
		}
		if base == "" {
			base = got
		} else if got != base {
			t.Fatal("formatting or explicit defaults changed semantic digest")
		}
	}
}
