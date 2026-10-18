package classification

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// MaxCodeownersBytes pins GitHub's 3 MB rejection boundary. Acquisition's
// smaller aggregate governing budget can conservatively return unknown sooner.
const MaxCodeownersBytes = 3 * 1024 * 1024

// Owner is a canonical CODEOWNERS token, not a resolved approver or group.
type Owner struct{ Kind, Name string }

// Label returns the classification token spelling fixed by ADR 0007.
func (o Owner) Label() string { return "owned:" + o.Kind + ":" + o.Name }

var ownerName = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\[bot\])?$`)
var teamSlug = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func canonicalOwner(token string) (Owner, error) {
	if strings.HasPrefix(token, "@") {
		name := strings.TrimPrefix(token, "@")
		parts := strings.Split(name, "/")
		if len(parts) == 1 && ownerName.MatchString(name) {
			return Owner{"user", strings.ToLower(name)}, nil
		}
		if len(parts) == 2 && ownerName.MatchString(parts[0]) && teamSlug.MatchString(parts[1]) {
			return Owner{"team", strings.ToLower(name)}, nil
		}
	} else if strings.Count(token, "@") == 1 {
		local, domain, _ := strings.Cut(token, "@")
		o := Owner{"email", local + "@" + strings.ToLower(domain)}
		address, err := mail.ParseAddress(token)
		if err == nil && address.Address == token && local != "" && domain != "" && cp.ValidClassificationLabel(o.Label()) {
			return o, nil
		}
	}
	return Owner{}, fmt.Errorf("invalid owner %q", token)
}

type ownerEntry struct {
	text        string
	pattern     *treeMatcher
	owners      []Owner
	diagnostics []string
}
type ownersFile struct {
	entries     []ownerEntry
	tokens      []Owner
	diagnostics []string
	valid       bool
}

func parseOwners(content string) ownersFile {
	out := ownersFile{valid: true}
	if len(content) >= MaxCodeownersBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		out.valid = false
		out.diagnostics = []string{"CODEOWNERS invalid or at/over 3 MB"}
		return out
	}
	seen := map[Owner]bool{}
	for line, text := range strings.Split(content, "\n") {
		fields := ownerFields(text)
		if len(fields) == 0 {
			continue
		}
		entry := ownerEntry{text: text}
		for _, token := range fields[1:] {
			owner, err := canonicalOwner(token)
			if err != nil {
				entry.diagnostics = append(entry.diagnostics, err.Error())
				continue
			}
			entry.owners = append(entry.owners, owner)
			if !seen[owner] {
				seen[owner] = true
				out.tokens = append(out.tokens, owner)
			}
		}
		pattern, err := treePattern(fields[0], true)
		if err != nil {
			out.valid = false
			out.diagnostics = append(out.diagnostics, fmt.Sprintf("CODEOWNERS line %d: %v", line+1, err))
			continue
		}
		entry.pattern = pattern
		for _, diagnostic := range entry.diagnostics {
			out.diagnostics = append(out.diagnostics, fmt.Sprintf("CODEOWNERS line %d: %s", line+1, diagnostic))
		}
		out.entries = append(out.entries, entry)
	}
	return out
}

func (f ownersFile) effective(p string) *ownerEntry {
	for i := len(f.entries) - 1; i >= 0; i-- {
		if f.entries[i].pattern.MatchString(p) {
			return &f.entries[i]
		}
	}
	return nil
}
