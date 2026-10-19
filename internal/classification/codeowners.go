package classification

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// MaxCodeownersBytes pins GitHub's 3 MB rejection boundary. Acquisition's
// smaller aggregate governing budget can conservatively return unknown sooner.
const MaxCodeownersBytes = 3 * 1024 * 1024

// Bound compiled matchers and durable per-path owner evidence independently of
// the forge's acquisition budget. Exceeding any bound invalidates the file.
const (
	MaxOwnerEntries    = 1024
	MaxOwnerLineBytes  = 2048
	MaxOwnersPerEntry  = 32
	MaxOwnerTokens     = 256
	MaxOwnerTokenBytes = 128
)

// Owner is a canonical CODEOWNERS token, not a resolved approver or group.
type Owner struct{ Kind, Name string }

// Label returns the classification token spelling fixed by ADR 0007.
func (o Owner) Label() string { return "owned:" + o.Kind + ":" + o.Name }

var ownerName = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\[bot\])?$`)
var teamSlug = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func canonicalOwner(token string) (Owner, error) {
	if len(token) > MaxOwnerTokenBytes {
		return Owner{}, fmt.Errorf("invalid owner: token byte budget exceeded")
	}
	if strings.HasPrefix(token, "@") {
		name := strings.TrimPrefix(token, "@")
		parts := strings.Split(name, "/")
		if len(parts) == 1 && ownerName.MatchString(name) && cp.ValidClassificationLabel((Owner{"user", strings.ToLower(name)}).Label()) {
			return Owner{"user", strings.ToLower(name)}, nil
		}
		if len(parts) == 2 && ownerName.MatchString(parts[0]) && teamSlug.MatchString(parts[1]) && cp.ValidClassificationLabel((Owner{"team", strings.ToLower(name)}).Label()) {
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

func parseOwners(content string) (out ownersFile) {
	out = ownersFile{valid: true}
	defer func() { out.diagnostics = boundedDiagnostics(out.diagnostics) }()
	if len(content) >= MaxCodeownersBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) || strings.ContainsRune(content, '\ufeff') {
		out.valid = false
		out.diagnostics = []string{"CODEOWNERS invalid or at/over 3 MB"}
		return out
	}
	seen := map[Owner]bool{}
	nonempty := 0
	for line, text := range strings.Split(content, "\n") {
		fields := ownerFields(text)
		if len(fields) == 0 {
			continue
		}
		nonempty++
		if nonempty > MaxOwnerEntries || len(text) > MaxOwnerLineBytes || len(fields)-1 > MaxOwnersPerEntry || len(out.entries) >= MaxOwnerEntries {
			return ownersFile{diagnostics: []string{"CODEOWNERS entry/line/owner budget exceeded"}}
		}
		for _, field := range fields {
			for i := 0; i < len(field); i++ {
				if field[i] == '\\' {
					i++
					continue
				}
				if field[i] == '#' {
					return ownersFile{diagnostics: []string{"CODEOWNERS unescaped hash inside token"}}
				}
			}
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
		if len(out.tokens) > MaxOwnerTokens {
			return ownersFile{diagnostics: []string{"CODEOWNERS distinct owner budget exceeded"}}
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
		entry.diagnostics = boundedDiagnostics(entry.diagnostics)
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

// GoverningOwners exposes canonical user/team tokens for static policy identity
// pinning. Matching, precedence and syntax remain owned by this parser; serve
// must not invent a second CODEOWNERS tokenizer. Email tokens stay unresolved.
func GoverningOwners(g forge.GoverningFiles) ([]Owner, bool) {
	source, known := selectedOwners(g)
	parsed := parseOwners(source.Content)
	if !known || !parsed.valid {
		return nil, false
	}
	return append([]Owner(nil), parsed.tokens...), true
}
