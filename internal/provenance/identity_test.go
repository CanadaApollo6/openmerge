package provenance

import "testing"

func TestIdentitySpoofingAndClassConflicts(t *testing.T) {
	c, err := NewClassifier([]IdentityRule{{"bot", "123+coder[bot]@users.noreply.github.com", Agent}, {"human", "human@example.com", Human}, {"conflict-a", "same@example.com", Agent}, {"conflict-h", "SAME@example.com", Human}})
	if err != nil {
		t.Fatal(err)
	}
	bot := "123+coder[bot]@users.noreply.github.com"
	for _, tc := range []struct {
		name  string
		input IdentityInput
		want  Class
	}{
		{"author", IdentityInput{AuthorMailbox: bot}, Agent},
		{"committer", IdentityInput{CommitterMailbox: bot}, Agent},
		{"generated committer", IdentityInput{CommitterMailbox: bot, GeneratedCommitter: true}, Undeclared},
		{"positive human", IdentityInput{AuthorMailbox: " Human@EXAMPLE.COM\t"}, Human},
		{"human cannot erase agent", IdentityInput{AuthorMailbox: "human@example.com", CommitterMailbox: bot}, Mixed},
		{"conflicting rules", IdentityInput{AuthorMailbox: "same@example.com"}, Mixed},
		{"unknown", IdentityInput{AuthorMailbox: "unknown@example.com"}, Undeclared},
		{"display name spoof", IdentityInput{AuthorMailbox: "coder[bot] <unknown@example.com>"}, Undeclared},
		{"substring spoof", IdentityInput{AuthorMailbox: "x" + bot}, Undeclared},
		{"domain suffix spoof", IdentityInput{AuthorMailbox: bot + ".evil"}, Undeclared},
		{"unicode spoof", IdentityInput{AuthorMailbox: "humаn@example.com"}, Undeclared},
		{"coauthor", IdentityInput{ParsedTrailers: "CO-AUTHORED-BY: Anything <" + bot + ">"}, Agent},
		{"human coauthor alone", IdentityInput{ParsedTrailers: "Co-Authored-By: Person <human@example.com>"}, Undeclared},
		{"coauthor display spoof", IdentityInput{ParsedTrailers: "Co-Authored-By: " + bot + " <unknown@example.com>"}, Undeclared},
		{"multiple coauthors on one line", IdentityInput{ParsedTrailers: "Co-Authored-By: A <unknown@example.com>, B <" + bot + ">"}, Undeclared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(c.Declarations(tc.input)); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	if len(c.Declarations(IdentityInput{AuthorMailbox: "same@example.com"})) != 2 {
		t.Fatal("conflicting rule IDs lost")
	}
}

func TestIdentityConfigurationValidation(t *testing.T) {
	for _, rules := range [][]IdentityRule{
		{{"", "a@example.com", Agent}}, {{"a", "a@example.com", Mixed}}, {{"a", "A <a@example.com>", Agent}},
		{{"a", "a@example.com", Agent}, {"a", "b@example.com", Human}}, {{"a", "é@example.com", Agent}},
	} {
		if _, err := NewClassifier(rules); err == nil {
			t.Fatal("invalid identity rules accepted", rules)
		}
	}
}
