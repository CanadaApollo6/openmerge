package provenance

import (
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
)

// IdentityRule maps an exact mailbox to a positive declaration class. Bot
// accounts require an explicitly configured mailbox; no forge lookup is made.
type IdentityRule struct {
	ID, Mailbox string
	Class       Class
}

// Classifier holds validated immutable owner configuration.
type Classifier struct{ rules []IdentityRule }

// NormalizeMailbox lowercases ASCII local and domain parts and trims surrounding
// ASCII space/tab. It rejects display names, non-ASCII, comments and quoted local
// parts. It does not strip plus tags, dots, or GitHub noreply account IDs.
func NormalizeMailbox(value string) (string, error) {
	value = strings.Trim(value, " \t")
	for _, c := range value {
		if c <= 32 || c >= 127 || strings.ContainsRune("<>(),;:\\\"", c) {
			return "", fmt.Errorf("expected a bare ASCII mailbox")
		}
	}
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", fmt.Errorf("expected a bare ASCII mailbox")
	}
	for _, c := range domain {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return "", fmt.Errorf("expected an ASCII mailbox domain")
		}
	}
	return ascii.Lower(value), nil
}

// NewClassifier rejects duplicate rule IDs. Conflicting classes on the same
// mailbox are retained as mixed evidence rather than resolved by rule order.
func NewClassifier(rules []IdentityRule) (Classifier, error) {
	out := Classifier{}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || seen[r.ID] || r.Class != Agent && r.Class != Human {
			return Classifier{}, fmt.Errorf("invalid or duplicate identity rule %q", r.ID)
		}
		mailbox, err := NormalizeMailbox(r.Mailbox)
		if err != nil {
			return Classifier{}, err
		}
		r.Mailbox = mailbox
		seen[r.ID] = true
		out.rules = append(out.rules, r)
	}
	return out, nil
}

// IdentityInput contains original Git identities. GeneratedCommitter must come
// from the trusted build mapping, never a contributor-supplied replay trailer.
// ParsedTrailers is the same pinned Git --parse --no-divider output consumed
// by ParseGitTrailers.
type IdentityInput struct {
	AuthorMailbox, CommitterMailbox string
	ParsedTrailers                  GitTrailerOutput
	GeneratedCommitter              bool
}

// Declarations implements only the provenance mailbox exception. It cannot
// produce forge identities, permissions, accountable humans or reviewer pools.
func (c Classifier) Declarations(in IdentityInput) []Declaration {
	var out []Declaration
	match := func(kind, value string, agentOnly bool) {
		mailbox, err := NormalizeMailbox(value)
		if err != nil {
			return
		}
		for _, rule := range c.rules {
			if mailbox == rule.Mailbox && (!agentOnly || rule.Class == Agent) {
				out = append(out, Declaration{Source: "git-identities", Version: ClassifierVersion, Kind: kind, Value: mailbox, Outcome: NotRequested, Class: rule.Class, RuleID: rule.ID})
			}
		}
	}
	match("author", in.AuthorMailbox, false)
	if !in.GeneratedCommitter {
		match("committer", in.CommitterMailbox, false)
	}
	for _, t := range gitTrailerPairs(in.ParsedTrailers) {
		if t.key != "co-authored-by" {
			continue
		}
		// Require the Git Name <mailbox> shape, exactly one address, no comments.
		left := strings.LastIndex(t.value, "<")
		if left < 0 || !strings.HasSuffix(t.value, ">") || strings.ContainsAny(t.value[:left], "<>") {
			continue
		}
		match("co-authored-by", t.value[left+1:len(t.value)-1], true)
	}
	return canonicalDeclarations(out)
}
