package dependency

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

func TestAuthoritativeIntentBoundaries(t *testing.T) {
	body, err := os.ReadFile("testdata/intent.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(body), strings.ReplaceAll(string(body), "\n", "\r\n")} {
		inputs, err := ParseIntent(text, testScope())
		if err != nil {
			t.Fatal(err)
		}
		var aliases []string
		for _, input := range inputs {
			aliases = append(aliases, input.Alias)
		}
		want := []string{"https://github.com/acme/api/pull/1", "https://github.com/acme/api/pull/2", "https://github.com/acme/api/pull/3"}
		if !reflect.DeepEqual(aliases, want) {
			t.Fatalf("got %#v", aliases)
		}
	}
}

func TestMalformedRecognizedIntentFailsClosed(t *testing.T) {
	for _, body := range []string{"Depends-On:", "Depends-On: bad", "Depends-On: #42", "Depends-On: github:acme/api#42", "Depends-On: https://github.com/acme/api/pull/1 extra", "Depends-On: https://github.com/acme/api/pull/1\nDepends-On:"} {
		if got, err := ParseIntent(body, testScope()); err == nil || got != nil {
			t.Fatalf("partial/accepted %q: %#v %v", body, got, err)
		}
	}
}

func intentCorpus(t testing.TB) []struct {
	Name    string
	Body    string
	Aliases []string
} {
	t.Helper()
	data, err := os.ReadFile("testdata/intent-commonmark.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string
		Body    string
		Aliases []string
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestIntentCommonMarkCorpus(t *testing.T) {
	for _, tc := range intentCorpus(t) {
		t.Run(tc.Name, func(t *testing.T) {
			inputs, err := ParseIntent(tc.Body, testScope())
			if err != nil {
				t.Fatal(err)
			}
			aliases := []string{}
			for _, input := range inputs {
				aliases = append(aliases, input.Alias)
			}
			if !reflect.DeepEqual(aliases, tc.Aliases) {
				t.Fatalf("aliases %q; want %q", aliases, tc.Aliases)
			}
		})
	}
}

// Check selected source lines against an independent walk of the entire AST,
// including excluded containers. A declaration must never overlap any forbidden
// node, even through lazy continuations or multiline inline nodes.
func assertIntentProseLines(t testing.TB, body string) {
	t.Helper()
	source := []byte(strings.ReplaceAll(body, "\r\n", "\n"))
	lines := intentLines(source)
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		forbidden := false
		for ancestor := n; ancestor != nil; ancestor = ancestor.Parent() {
			switch ancestor.Kind() {
			case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindBlockquote,
				ast.KindHTMLBlock, ast.KindCodeSpan, ast.KindRawHTML, ast.KindList:
				forbidden = true
			}
		}
		if !forbidden {
			return ast.WalkContinue, nil
		}
		var segments []text.Segment
		switch node := n.(type) {
		case *ast.Text:
			segments = append(segments, node.Segment)
		case *ast.RawHTML:
			for i := 0; i < node.Segments.Len(); i++ {
				segments = append(segments, node.Segments.At(i))
			}
		default:
			if n.Type() == ast.TypeBlock {
				for i := 0; i < n.Lines().Len(); i++ {
					segments = append(segments, n.Lines().At(i))
				}
			}
		}
		for _, span := range segments {
			for _, line := range lines {
				if span.Start < line.Stop && line.Start < span.Stop {
					t.Fatalf("selected line %q overlaps %s node %q", line.Value(source), n.Kind(), source[span.Start:span.Stop])
				}
			}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every returned input must correspond to a selected, unmodified source line.
	inputs, err := ParseIntent(body, testScope())
	if err != nil {
		return
	}
	var want []Input
	for _, line := range lines {
		raw := string(bytes.TrimSuffix(line.Value(source), []byte{'\n'}))
		trimmed := strings.TrimLeft(raw, " ")
		if len(raw)-len(trimmed) > 3 {
			continue
		}
		value, ok := strings.CutPrefix(trimmed, "Depends-On:")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) != 1 {
			t.Fatal("malformed selected declaration succeeded")
		}
		input, err := parseInput(fields[0], testScope())
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, input)
	}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("inputs %v; want source inputs %v", inputs, want)
	}
}
