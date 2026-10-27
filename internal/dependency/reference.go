// Package dependency contains inert ADR 0011 candidate contracts. It performs
// no I/O and is not imported by live paths. A valid graph is not landing authority.
package dependency

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scope is supplied by configured identity, never inferred from declaration text.
// Namespace construction is explicit in NewJournalIdentity; persistence is unwired.
type Scope struct {
	ForgeInstance    string
	Installation     string
	JournalNamespace string
}

// Reference identifies a journal-scoped change. Revision zero means revision-less.
type Reference struct {
	Scope    Scope
	ChangeID string
	Revision uint64
}

// Errors are component diagnostics; callers must redact them for unauthorized views.
var (
	ErrMalformed  = errors.New("dependency-unresolved: malformed reference")
	ErrOutOfScope = errors.New("dependency-out-of-scope")
	ErrUnresolved = errors.New("dependency-unresolved")
	ErrConflict   = errors.New("conflicting dependency modes or pins")
	ErrLimit      = errors.New("dependency graph limit exceeded")
)

func validDigestID(s, prefix string) bool {
	if len(s) != len(prefix)+64 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, c := range s[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func cleanText(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) })
}

func segmentText(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func originURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") || u.Host != strings.ToLower(u.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || !cleanText(raw) || u.String() != raw || strings.Contains(raw, "#") {
		return nil, ErrMalformed
	}
	if strings.HasSuffix(u.Hostname(), ".") || u.Port() == "443" {
		return nil, ErrMalformed
	}
	if port := u.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 || strconv.FormatUint(number, 10) != port {
			return nil, ErrMalformed
		}
	}
	return u, nil
}

// Validate checks identity syntax without reading a database or registry.
func (s Scope) Validate() error {
	driver, origin, ok := strings.Cut(s.ForgeInstance, ":")
	if !ok || !name(driver) || !segmentText(s.Installation) || !validDigestID(s.JournalNamespace, "omj1_") {
		return ErrMalformed
	}
	_, err := originURL(origin)
	return err
}

// EscapeSegment implements RFC 3986 unreserved-only encoding, with uppercase hex.
// url.PathEscape leaves reserved characters unescaped and is unsuitable here.
func EscapeSegment(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c)) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}

func decodeSegment(s string) (string, error) {
	decoded, err := url.PathUnescape(s)
	if err != nil || !segmentText(decoded) || EscapeSegment(decoded) != s {
		return "", ErrMalformed
	}
	return decoded, nil
}

// Canonical returns the sole accepted spelling of a reference.
func (r Reference) Canonical() (string, error) {
	if err := r.Scope.Validate(); err != nil {
		return "", err
	}
	if !validDigestID(r.ChangeID, "chg1_") {
		return "", ErrMalformed
	}
	raw := "openmerge-change:v1/" + EscapeSegment(r.Scope.ForgeInstance) + "/" + EscapeSegment(r.Scope.Installation) + "/" + r.Scope.JournalNamespace + "/" + r.ChangeID
	if r.Revision != 0 {
		raw += "@" + strconv.FormatUint(r.Revision, 10)
	}
	return raw, nil
}

// ParseReference rejects foreign scope before the caller can look up an ID.
func ParseReference(raw string, local Scope) (Reference, error) {
	if err := local.Validate(); err != nil {
		return Reference{}, err
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 5 || parts[0] != "openmerge-change:v1" {
		return Reference{}, ErrMalformed
	}
	forge, err := decodeSegment(parts[1])
	if err != nil {
		return Reference{}, err
	}
	installation, err := decodeSegment(parts[2])
	if err != nil {
		return Reference{}, err
	}
	scope := Scope{forge, installation, parts[3]}
	if err := scope.Validate(); err != nil {
		return Reference{}, err
	}
	if scope != local {
		return Reference{}, ErrOutOfScope
	}
	id, pin, pinned := strings.Cut(parts[4], "@")
	r := Reference{Scope: scope, ChangeID: id}
	if pinned {
		revision, err := strconv.ParseUint(pin, 10, 64)
		if err != nil || revision == 0 || strconv.FormatUint(revision, 10) != pin {
			return Reference{}, ErrMalformed
		}
		r.Revision = revision
	}
	canonical, err := r.Canonical()
	if err != nil || canonical != raw {
		return Reference{}, ErrMalformed
	}
	return r, nil
}

// Input is exactly one canonical reference or a recognized revision-less URL alias.
type Input struct {
	Reference Reference
	Alias     string
}

func name(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func repositoryURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, ErrMalformed
	}
	origin := "https://" + u.Host
	if _, err := originURL(origin); err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.String() != raw {
		return nil, ErrMalformed
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || !name(parts[0]) || !name(parts[1]) || u.RawPath != "" || strings.Contains(raw, "%") {
		return nil, ErrMalformed
	}
	return u, nil
}

func parseAlias(raw string, scope Scope) (string, error) {
	driver, origin, _ := strings.Cut(scope.ForgeInstance, ":")
	if driver != "github" {
		return "", ErrOutOfScope
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrMalformed
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 5 || parts[3] != "pull" {
		return "", ErrMalformed
	}
	number, err := strconv.ParseUint(parts[4], 10, 64)
	if err != nil || number == 0 || strconv.FormatUint(number, 10) != parts[4] {
		return "", ErrMalformed
	}
	repo := "https://" + u.Host + "/" + parts[1] + "/" + parts[2]
	if _, err := repositoryURL(repo); err != nil || u.User != nil || u.Scheme != "https" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || raw != repo+"/pull/"+parts[4] {
		return "", ErrMalformed
	}
	if "https://"+u.Host != origin {
		return "", ErrOutOfScope
	}
	return repo, nil
}

func parseInput(raw string, scope Scope) (Input, error) {
	if strings.HasPrefix(raw, "openmerge-change:") {
		ref, err := ParseReference(raw, scope)
		return Input{Reference: ref}, err
	}
	if _, err := parseAlias(raw, scope); err != nil {
		return Input{}, err
	}
	return Input{Alias: raw}, nil
}

// Normalize sorts, deduplicates, and rejects mixed modes or pins for one change.
// The edge limit counts distinct resolved changes, not duplicate textual lines.
func Normalize(refs []Reference, scope Scope) ([]Reference, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	byID := map[string]Reference{}
	for _, ref := range refs {
		if _, err := ref.Canonical(); err != nil {
			return nil, err
		}
		if ref.Scope != scope {
			return nil, ErrOutOfScope
		}
		if previous, ok := byID[ref.ChangeID]; ok && previous.Revision != ref.Revision {
			return nil, fmt.Errorf("%w: %s", ErrConflict, ref.ChangeID)
		}
		byID[ref.ChangeID] = ref
		if len(byID) > MaxDirectEdges {
			return nil, ErrLimit
		}
	}
	out := make([]Reference, 0, len(byID))
	for _, ref := range byID {
		out = append(out, ref)
	}
	sortReferences(out)
	return out, nil
}
