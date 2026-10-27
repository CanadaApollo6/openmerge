package dependency

import (
	"encoding/json"
	"errors"
	"html"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func TestAuthoritativeIntentBoundaries(t *testing.T) {
	body, err := os.ReadFile("testdata/intent.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(body), strings.ReplaceAll(string(body), "\n", "\r\n"), "\ufeff" + strings.ReplaceAll(string(body), "\n", "\r")} {
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
	Name      string
	Body      string
	Aliases   []string
	Ambiguous bool
} {
	t.Helper()
	data, err := os.ReadFile("testdata/intent-commonmark.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string
		Body      string
		Aliases   []string
		Ambiguous bool
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
			if tc.Ambiguous {
				if !errors.Is(err, ErrDeclarationAmbiguous) || inputs != nil {
					t.Fatalf("got %v, %v; want ambiguity without partial inputs", inputs, err)
				}
				inputs, err = ParseIntent("Depends-On: https://github.com/acme/api/pull/1\n\n"+tc.Body, testScope())
				if !errors.Is(err, ErrDeclarationAmbiguous) || inputs != nil {
					t.Fatalf("ambiguity leaked partial inputs: %v, %v", inputs, err)
				}
				return
			}
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

// Independently decode the broad candidate grammar. The fuzz oracle must catch
// omissions in production detection as well as in AST/source classification.
func oracleDeclarationCandidate(line string) bool {
	var unescaped strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", rune(line[i+1])) {
			i++
		}
		unescaped.WriteByte(line[i])
	}
	decoded := cases.Fold().String(norm.NFKC.String(html.UnescapeString(unescaped.String())))
	var key strings.Builder
	for _, r := range decoded {
		switch {
		case unicode.Is(unicode.Dash, r) || strings.ContainsRune("\u00ad\u02d7\u2043\u2212\u2796", r):
			key.WriteByte('-')
		case strings.ContainsRune("\u02d0\u02f8\u0589\u05c3\u205a\u2236\ua789", r):
			key.WriteByte(':')
		default:
			key.WriteRune(r)
		}
	}
	return strings.Contains(key.String(), "depends-on:")
}

// Independently derive every possible successful input from whole paragraphs.
// Eligibility and ignored source coverage are derived without intentLines.
// Keep footnote definitions in this oracle AST, including unreferenced ones.
func assertIntentProseLines(t testing.TB, body string) {
	t.Helper()
	inputs, err := ParseIntent(body, testScope())
	if err != nil {
		if inputs != nil {
			t.Fatal("error returned partial inputs")
		}
		return
	}
	if utf8.RuneCountInString(body) > MaxBodyCharacters {
		t.Fatal("oversized body succeeded")
	}
	body = strings.TrimPrefix(body, "\ufeff")
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	source := []byte(body)
	physicalLines := strings.Split(body, "\n")
	doc := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithBlockParsers(util.Prioritized(extension.NewFootnoteBlockParser(), 999)),
			parser.WithInlineParsers(util.Prioritized(extension.NewFootnoteParser(), 101)),
		),
	).Parser().Parse(text.NewReader(source))
	ignored := make([]bool, len(source))
	accepted := make([]bool, len(physicalLines))
	cover := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			ignored[i] = true
		}
	}
	var want []Input
	walkErr := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		for ancestor := n; ancestor != nil; ancestor = ancestor.Parent() {
			switch ancestor.Kind() {
			case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindBlockquote, ast.KindHTMLBlock:
				if n.Type() == ast.TypeBlock {
					for i := 0; i < n.Lines().Len(); i++ {
						cover(n.Lines().At(i))
					}
				}
			}
		}
		if block, ok := n.(*ast.HTMLBlock); ok && block.HasClosure() {
			cover(block.ClosureLine)
		}
		if fence, ok := n.(*ast.FencedCodeBlock); ok && fence.Info != nil {
			cover(fence.Info.Segment)
		}
		if raw, ok := n.(*ast.RawHTML); ok && raw.Segments.Len() > 0 {
			first := raw.Segments.At(0)
			if !strings.HasPrefix(string(first.Value(source)), "<!--") {
				return ast.WalkContinue, nil
			}
			for i := 0; i < raw.Segments.Len(); i++ {
				cover(raw.Segments.At(i))
			}
		}
		if n.Kind() != ast.KindParagraph || n.Parent() != doc {
			return ast.WalkContinue, nil
		}
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			if child.Kind() != ast.KindText && child.Kind() != ast.KindAutoLink {
				return ast.WalkContinue, nil
			}
		}
		var paragraph []Input
		var lineNumbers []int
		var refs []string
		for i := 0; i < n.Lines().Len(); i++ {
			segment := n.Lines().At(i)
			lineNumber := strings.Count(body[:segment.Start], "\n")
			raw := physicalLines[lineNumber]
			line := strings.TrimLeft(raw, " ")
			if len(raw)-len(line) > 3 || !strings.HasPrefix(line, "Depends-On:") {
				return ast.WalkContinue, nil
			}
			fields := strings.Fields(strings.TrimPrefix(line, "Depends-On:"))
			if len(fields) != 1 {
				return ast.WalkContinue, nil
			}
			input, parseErr := parseInput(fields[0], testScope())
			if parseErr != nil {
				t.Fatalf("malformed pure declaration paragraph succeeded: %v", parseErr)
			}
			paragraph = append(paragraph, input)
			lineNumbers = append(lineNumbers, lineNumber)
			refs = append(refs, fields[0])
		}
		for child := n.FirstChild(); child != nil; child = child.NextSibling() {
			if link, ok := child.(*ast.AutoLink); ok {
				label := string(link.Label(source))
				matches := false
				for _, ref := range refs {
					matches = matches || ref == label
				}
				if !matches || link.AutoLinkType != ast.AutoLinkURL || string(link.URL(source)) != label {
					return ast.WalkContinue, nil
				}
			}
		}
		for _, line := range lineNumbers {
			accepted[line] = true
		}
		want = append(want, paragraph...)
		return ast.WalkContinue, nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	// A successful parse must account for every decoded candidate, even when
	// Markdown omits it from visible text. Do not merely check produced inputs.
	offset := 0
	for number, line := range physicalLines {
		if !accepted[number] {
			var visible strings.Builder
			for i := range len(line) {
				if ignored[offset+i] {
					visible.WriteByte('\n') // Separate regions without joining text.
				} else {
					visible.WriteByte(line[i])
				}
			}
			for _, fragment := range strings.Split(visible.String(), "\n") {
				if oracleDeclarationCandidate(fragment) {
					t.Fatalf("silently dropped candidate on line %d: %q", number+1, line)
				}
			}
		}
		offset += len(line) + 1
	}
	if !reflect.DeepEqual(inputs, want) {
		t.Fatalf("inputs %v; want only pure declaration paragraphs %v", inputs, want)
	}
}

func TestIntentAmbiguousDeclarations(t *testing.T) {
	const declaration = "Depends-On: https://github.com/acme/api/pull/42"
	for _, tc := range []struct{ name, body string }{
		{"bare CR mixed prose", "x\r" + declaration},
		{"mixed prose", "Example\n" + declaration},
		{"prose after", declaration + "\nExample"},
		{"indented continuation", declaration + "\n    " + declaration},
		{"list item", "- " + declaration},
		{"ordered list", "1. " + declaration},
		{"inline code HTML", "Example <code>\n" + declaration + "\n</code>"},
		{"inline pre HTML", "Example <pre>\n" + declaration + "\n</pre>"},
		{"link title", "[example](https://example.com \"\n" + declaration + "\n\")"},
		{"image title", "![example](https://example.com/a.png \"\n" + declaration + "\n\")"},
		{"image alt", "![\n" + declaration + "\n](https://example.com/a.png)"},
		{"reference title", "[example]: https://example.com \"\n" + declaration + "\n\""},
		{"emphasis", "Depends-On: *https://github.com/acme/api/pull/42*"},
		{"code span", "Depends-On: `https://github.com/acme/api/pull/42`"},
		{"comment after", declaration + " <!-- example -->"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A preceding valid paragraph must not leak a partial declaration.
			inputs, err := ParseIntent(declaration+"\n\n"+tc.body, testScope())
			if !errors.Is(err, ErrDeclarationAmbiguous) || inputs != nil || !strings.Contains(err.Error(), "own paragraph, with nothing else") {
				t.Fatalf("got %v, %v; want actionable ambiguity without partial inputs", inputs, err)
			}
		})
	}
}

func TestIntentPureDeclarationParagraph(t *testing.T) {
	body := "Depends-On: https://github.com/acme/api/pull/1\nDepends-On: https://github.com/acme/api/pull/2  \nDepends-On: https://github.com/acme/api/pull/3"
	inputs, err := ParseIntent(body, testScope())
	if err != nil || len(inputs) != 3 {
		t.Fatalf("got %v, %v", inputs, err)
	}
	for i, input := range inputs {
		if input.Alias != "https://github.com/acme/api/pull/"+string(rune('1'+i)) {
			t.Fatal(inputs)
		}
	}
}

func TestIntentLinkifyPreservesExactReference(t *testing.T) {
	const ref = "https://github.com/acme/api/pull/42"
	body := []byte("Depends-On: " + ref)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Footnote)).Parser().Parse(text.NewReader(body))
	links := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if link, ok := n.(*ast.AutoLink); entering && ok {
			links++
			if string(link.Label(body)) != ref || string(link.URL(body)) != ref {
				t.Fatalf("linkify changed the literal reference: %q -> %q", link.Label(body), link.URL(body))
			}
		}
		return ast.WalkContinue, nil
	})
	if links != 1 {
		t.Fatalf("expected GFM URL autolink, got %d", links)
	}
	inputs, err := ParseIntent(string(body), testScope())
	if err != nil || len(inputs) != 1 || inputs[0].Alias != ref {
		t.Fatalf("linkify declaration: %v, %v", inputs, err)
	}
}

func TestIntentBareCRFence(t *testing.T) {
	inputs, err := ParseIntent("```\rtext `\nDepends-On: https://github.com/acme/api/pull/42", testScope())
	if err != nil || len(inputs) != 0 {
		t.Fatalf("unclosed fence must ignore example: %v, %v", inputs, err)
	}
}

func TestIntentBodyCharacterLimit(t *testing.T) {
	const declaration = "Depends-On: https://github.com/acme/api/pull/42"
	for _, char := range []string{"x", "é", "😀"} {
		body := strings.Repeat(char, MaxBodyCharacters-2-len(declaration)) + "\n\n" + declaration
		inputs, err := ParseIntent(body, testScope())
		if err != nil || len(inputs) != 1 {
			t.Fatalf("exact character cap: %v, %v", inputs, err)
		}
		inputs, err = ParseIntent(body+char, testScope())
		if !errors.Is(err, ErrBodyLimit) || inputs != nil {
			t.Fatalf("character overflow: %v, %v", inputs, err)
		}
	}
	// The cap must win before scope validation or expensive Markdown parsing.
	if inputs, err := ParseIntent(strings.Repeat("[", MaxBodyCharacters+1), Scope{}); !errors.Is(err, ErrBodyLimit) || inputs != nil {
		t.Fatalf("oversized adversarial body: %v, %v", inputs, err)
	}
}
