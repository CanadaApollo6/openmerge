package provenance

import (
	"strings"
	"unicode/utf8"
)

// MaxAttributionBytes counts the entire UTF-8 block, including line terminators.
const MaxAttributionBytes = 64 << 10

// RetainAttribution adds original recognized provenance to an existing generated
// attribution block. Existing coauthors, sign-offs and authoritative OpenMerge-*
// suffixes count toward the same bound. The caller supplies only the block, not
// prose, and inserts the returned block once. Nothing changes live builders.
func RetainAttribution(existing []string, inventory Inventory) ([]string, Diagnostic) {
	fail := func(outcome ReadOutcome, resource string) ([]string, Diagnostic) {
		return nil, Diagnostic{Outcome: outcome, Resource: resource}
	}
	if !inventory.Complete {
		return fail(ReadPending, "inventory")
	}
	for _, commit := range inventory.Commits {
		if !commit.Complete {
			return fail(ReadPending, "inventory")
		}
	}
	if len(existing) > MaxAttributionBytes {
		return fail(LimitExceeded, "attribution_bytes")
	}
	inputBytes := 0
	for _, line := range existing {
		if len(line)+1 > MaxAttributionBytes-inputBytes {
			return fail(LimitExceeded, "attribution_bytes")
		}
		inputBytes += len(line) + 1
	}
	var ordinary, reserved []string
	seen := map[string]bool{}
	bytes := 0
	add := func(line string) Diagnostic {
		if !utf8.ValidString(line) || strings.ContainsAny(line, "\x00\r\n") {
			return Diagnostic{Outcome: InvalidInput, Resource: "attribution_encoding"}
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			return Diagnostic{Outcome: InvalidInput, Resource: "attribution_shape"}
		}
		key = strings.ToLower(key)
		canonical := key + ":" + value
		if seen[canonical] {
			return Diagnostic{}
		}
		if len(line)+1 > MaxAttributionBytes-bytes {
			return Diagnostic{Outcome: LimitExceeded, Resource: "attribution_bytes"}
		}
		seen[canonical] = true
		bytes += len(line) + 1
		if strings.HasPrefix(key, "openmerge-") {
			reserved = append(reserved, line)
		} else {
			ordinary = append(ordinary, line)
		}
		return Diagnostic{}
	}
	for _, line := range existing {
		if d := add(line); d.Outcome != "" {
			return nil, d
		}
	}
	for _, commit := range inventory.Commits {
		for _, declaration := range commit.Declarations {
			if declaration.Source != "git-trailers" || (declaration.Kind != "agent-session" && declaration.Kind != "entire-checkpoint") {
				continue
			}
			// Overlong/invalid UTF-8 values are retained only as digests in evidence.
			// They cannot be recreated as a silently shortened valid trailer.
			if declaration.Outcome == ValueTooLong {
				return fail(LimitExceeded, "attribution_value")
			}
			if declaration.Outcome == InvalidEncoding {
				return fail(InvalidInput, "attribution_encoding")
			}
			if d := add(declaration.Kind + ": " + declaration.Value); d.Outcome != "" {
				return nil, d
			}
		}
	}

	return append(ordinary, reserved...), Diagnostic{Outcome: ReadComplete}
}
