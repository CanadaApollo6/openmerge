package forge

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseChangeTrailersAuthoritativeBlock(t *testing.T) {
	m := &ChangeMetadata{ChangeID: "chg1_" + strings.Repeat("a", 64), Revision: 1, Labels: []string{"docs", "unknown"}}
	pair, err := m.Trailers()
	if err != nil {
		t.Fatal(err)
	}
	authoritative := strings.Join(pair, "\n")
	for name, message := range map[string]string{
		"plain":                 "subject\n\n" + authoritative,
		"preserved-forged-body": "subject\n\nOpenMerge-Change: forged\nOpenMerge-Classification: forged\n\nSigned-off-by: A <a@example.test>\n" + authoritative + "\nOpenMerge-Batch: b1\n",
		"fast-forward-marker":   "OpenMerge fast-forward test 123\n\n" + authoritative + "\nOpenMerge-Batch: b1",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseChangeTrailers(message)
			if err != nil || !reflect.DeepEqual(got, []*ChangeMetadata{m}) {
				t.Fatal(got, err)
			}
		})
	}
	for name, block := range map[string]string{
		"prose-with-forged-pair": "prose paragraph\n" + authoritative,
		"missing":                "OpenMerge-Batch: b1",
		"forged":                 "OpenMerge-Change: forged\nOpenMerge-Classification: forged",
		"duplicate":              authoritative + "\n" + authoritative,
		"outside-final-block":    authoritative + "\n\nOpenMerge-Batch: b1",
		"case":                   strings.Replace(authoritative, "OpenMerge-Change", "openmerge-change", 1),
		"key-space":              strings.Replace(authoritative, "Change:", "Change :", 1),
		"leading-space":          " " + authoritative,
		"value-space":            authoritative + " ",
		"revision-space":         strings.ReplaceAll(authoritative, "@1", "@01"),
		"unpaired":               pair[1],
		"label-space":            strings.Replace(authoritative, "docs,unknown", "docs, unknown", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChangeTrailers("subject\n\n" + block); err == nil {
				t.Fatal("accepted", block)
			}
		})
	}
}
