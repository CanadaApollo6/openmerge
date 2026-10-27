package dependency

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"slices"
	"sort"
	"strings"
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

// MaxBodyCharacters bounds the authoritative PR body before Markdown parsing.
const MaxBodyCharacters = 65_536

// Intent errors block the entire declaration, without returning partial inputs.
var (
	ErrBodyLimit            = errors.New("declaration-body-limit: PR body exceeds 65536 characters")
	ErrDeclarationAmbiguous = errors.New("declaration-ambiguous: put each Depends-On: declaration in its own paragraph, with nothing else")
)

func sortReferences(refs []Reference) {
	slices.SortFunc(refs, func(a, b Reference) int { return strings.Compare(a.ChangeID, b.ChangeID) })
}

func normalizeIntent(body string) []byte {
	body = strings.TrimPrefix(body, "\ufeff")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return []byte(strings.ReplaceAll(body, "\r", "\n"))
}

// ParseIntent accepts only top-level paragraphs consisting entirely of plain
// Depends-On: source lines. Other declaration-shaped lines block, except examples
// in code blocks, block quotes, HTML blocks and HTML comments. Commits/comments
// are never scanned. Errors return no partial result.
func ParseIntent(body string, scope Scope) ([]Input, error) {
	if utf8.RuneCountInString(body) > MaxBodyCharacters {
		return nil, ErrBodyLimit
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	source := normalizeIntent(body)
	lines, err := intentLines(source)
	if err != nil {
		return nil, err
	}
	var out []Input
	for _, segment := range lines {
		value, _ := strings.CutPrefix(strings.TrimSpace(string(segment.Value(source))), "Depends-On:")
		input, err := parseInput(strings.TrimSpace(value), scope)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", bytes.Count(source[:segment.Start], []byte{'\n'})+1, err)
		}
		out = append(out, input)
	}
	return out, nil
}

// declarationCandidate deliberately recognizes more spellings than we accept.
// Searching anywhere also catches keys after container markers and in hidden
// Markdown metadata; decoding here never widens the accepted source grammar.
func declarationCandidate(line []byte) bool {
	line = bytes.TrimLeft(line, " \t>#+-*0123456789.)[]")
	decoded := html.UnescapeString(string(util.UnescapePunctuations(line)))
	decoded = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, decoded)
	decoded = cases.Fold().String(norm.NFKC.String(decoded))
	decoded = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Dash, r) {
			return '-'
		}
		switch r {
		case '\u02d7', '\u2043', '\u2212', '\u2796':
			return '-'
		case '\u02d0', '\u02f8', '\u0589', '\u05c3', '\u205a', '\u2236', '\ua789':
			return ':'
		}
		return r
	}, decoded)
	return strings.Contains(decoded, "depends-on:")
}

// renderedIntentText joins inline text through formatting, links and code spans,
// dropping raw HTML tags/comments. Block and line boundaries stay separate.
func renderedIntentText(block ast.Node, source []byte) []byte {
	var rendered bytes.Buffer
	for child := block.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Type() != ast.TypeInline {
			continue
		}
		_ = ast.Walk(child, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch inline := node.(type) {
			case *ast.Text:
				rendered.Write(inline.Segment.Value(source))
				if inline.SoftLineBreak() || inline.HardLineBreak() {
					rendered.WriteByte('\n')
				}
			case *ast.String:
				rendered.Write(inline.Value)
			case *ast.AutoLink:
				rendered.Write(inline.Label(source))
			case *ast.RawHTML:
				return ast.WalkSkipChildren, nil
			}
			return ast.WalkContinue, nil
		})
	}
	return rendered.Bytes()
}

// intentExamples runs before Goldmark removes unreferenced footnotes, retaining
// both ignored source coverage and rendered candidates from hidden definitions.
type intentExamples struct {
	ignored  []bool
	rendered []ast.Node
}

func (e *intentExamples) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()
	ignore := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			e.ignored[i] = true
		}
	}
	depth := 0
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		example := false
		switch node.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindBlockquote, ast.KindHTMLBlock:
			example = true
		}
		if !entering {
			if example {
				depth--
			}
			return ast.WalkContinue, nil
		}
		if example {
			depth++
		}
		if depth == 0 && node.Type() == ast.TypeBlock && declarationCandidate(renderedIntentText(node, source)) {
			e.rendered = append(e.rendered, node)
		}
		if depth > 0 && node.Type() == ast.TypeBlock {
			for i := 0; i < node.Lines().Len(); i++ {
				ignore(node.Lines().At(i))
			}
		}
		if block, ok := node.(*ast.HTMLBlock); ok && block.HasClosure() {
			ignore(block.ClosureLine)
		}
		if fence, ok := node.(*ast.FencedCodeBlock); ok && fence.Info != nil {
			ignore(fence.Info.Segment)
		}
		if raw, ok := node.(*ast.RawHTML); ok && raw.Segments.Len() > 0 {
			first := raw.Segments.At(0)
			if !bytes.HasPrefix(first.Value(source), []byte("<!--")) {
				return ast.WalkContinue, nil
			}
			for i := 0; i < raw.Segments.Len(); i++ {
				ignore(raw.Segments.At(i))
			}
		}
		return ast.WalkContinue, nil
	})
}

// intentLines accepts exact physical lines, then independently checks every
// non-example source line for candidates, including text omitted from the AST.
func intentLines(source []byte) ([]text.Segment, error) {
	ignored := make([]bool, len(source))
	examples := &intentExamples{ignored: ignored}
	doc := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote),
		goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(examples, 998))),
	).Parser().Parse(text.NewReader(source))
	var physicalLines []text.Segment
	start := 0
	for _, line := range bytes.SplitAfter(source, []byte{'\n'}) {
		physicalLines = append(physicalLines, text.NewSegment(start, start+len(line)))
		start += len(line)
	}
	// Goldmark removes indentation from paragraph segments. Validate and return
	// the whole physical line so that trimming cannot widen the source grammar.
	physicalLineIndex := func(segment text.Segment) int {
		return sort.Search(len(physicalLines), func(i int) bool { return physicalLines[i].Start > segment.Start }) - 1
	}
	accepted := make([]bool, len(physicalLines))
	pureParagraphs := make(map[ast.Node]bool)
	var lines []text.Segment
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		paragraph, ok := node.(*ast.Paragraph)
		if !entering || !ok || paragraph.Parent() != doc {
			return ast.WalkContinue, nil
		}
		var refs []string
		for i := 0; i < paragraph.Lines().Len(); i++ {
			segment := physicalLines[physicalLineIndex(paragraph.Lines().At(i))]
			raw := string(segment.Value(source))
			trimmed := strings.TrimLeft(raw, " ")
			value, declaration := strings.CutPrefix(trimmed, "Depends-On:")
			fields := strings.Fields(value)
			if !declaration || len(raw)-len(trimmed) > 3 || len(fields) != 1 {
				return ast.WalkContinue, nil
			}
			refs = append(refs, fields[0])
		}
		for child := paragraph.FirstChild(); child != nil; child = child.NextSibling() {
			switch inline := child.(type) {
			case *ast.Text: // Soft and hard line breaks are flags on Text.
			case *ast.AutoLink:
				if inline.AutoLinkType != ast.AutoLinkURL ||
					!bytes.Equal(inline.Label(source), inline.URL(source)) ||
					!slices.Contains(refs, string(inline.Label(source))) {
					return ast.WalkContinue, nil
				}
			default:
				return ast.WalkContinue, nil
			}
		}
		for i := 0; i < paragraph.Lines().Len(); i++ {
			index := physicalLineIndex(paragraph.Lines().At(i))
			accepted[index] = true
			lines = append(lines, physicalLines[index])
		}
		pureParagraphs[paragraph] = true
		return ast.WalkContinue, nil
	})
	for _, block := range examples.rendered {
		if pureParagraphs[block] {
			continue
		}
		position := block.Pos()
		for ancestor := block.Parent(); position < 0 && ancestor != nil; ancestor = ancestor.Parent() {
			position = ancestor.Pos()
		}
		line := bytes.Count(source[:max(0, position)], []byte{'\n'}) + 1
		return nil, fmt.Errorf("line %d: %w", line, ErrDeclarationAmbiguous)
	}
	for index, segment := range physicalLines {
		if accepted[index] {
			continue
		}
		// Keep ignored spans separate in the source scan. The rendered pass
		// independently checks keys joined across inline comments or tags.
		for start := segment.Start; start < segment.Stop; {
			if ignored[start] {
				start++
				continue
			}
			stop := start + 1
			for stop < segment.Stop && !ignored[stop] {
				stop++
			}
			if declarationCandidate(source[start:stop]) {
				return nil, fmt.Errorf("line %d: %w", index+1, ErrDeclarationAmbiguous)
			}
			start = stop
		}
	}
	return lines, nil
}
