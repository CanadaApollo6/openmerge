package dependency

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
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

// intentLines keeps source spelling intact. The physical-line scan also sees
// hidden link/image titles and reference definitions that inline AST text omits.
// Block source segments additionally reveal declarations after list markers.
func intentLines(source []byte) ([]text.Segment, error) {
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	candidates := make([]bool, len(source)+1)
	ignored := make([]bool, len(source)+1)
	accepted := make([]bool, len(source)+1)
	candidate := func(segment text.Segment) {
		raw := segment.Value(source)
		trimmed := bytes.TrimLeft(raw, " \t")
		if bytes.HasPrefix(trimmed, []byte("Depends-On:")) {
			candidates[segment.Start+len(raw)-len(trimmed)] = true
		}
	}
	ignore := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			ignored[i] = true
		}
	}
	var physicalLines []text.Segment
	start := 0
	for _, line := range bytes.SplitAfter(source, []byte{'\n'}) {
		segment := text.NewSegment(start, start+len(line))
		physicalLines = append(physicalLines, segment)
		candidate(segment)
		start += len(line)
	}
	// Goldmark removes indentation from paragraph segments. Validate and return
	// the whole physical line so that trimming cannot widen the source grammar.
	physicalLine := func(segment text.Segment) text.Segment {
		i := sort.Search(len(physicalLines), func(i int) bool { return physicalLines[i].Start > segment.Start }) - 1
		return physicalLines[i]
	}
	var lines []text.Segment
	ignoredDepth := 0
	// The walk only classifies source; it cannot fail.
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		example := false
		switch node.Kind() {
		case ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindBlockquote, ast.KindHTMLBlock:
			example = true
		}
		if !entering {
			if example {
				ignoredDepth--
			}
			return ast.WalkContinue, nil
		}
		if example {
			ignoredDepth++
		}
		if node.Type() == ast.TypeBlock {
			for i := 0; i < node.Lines().Len(); i++ {
				segment := node.Lines().At(i)
				candidate(segment)
				if ignoredDepth > 0 {
					ignore(segment)
				}
			}
		}
		if html, ok := node.(*ast.RawHTML); ok && html.Segments.Len() > 0 {
			first := html.Segments.At(0)
			if bytes.HasPrefix(first.Value(source), []byte("<!--")) {
				for i := 0; i < html.Segments.Len(); i++ {
					ignore(html.Segments.At(i))
				}
			}
		}
		paragraph, ok := node.(*ast.Paragraph)
		if !ok || paragraph.Parent() != doc {
			return ast.WalkContinue, nil
		}
		for child := paragraph.FirstChild(); child != nil; child = child.NextSibling() {
			if _, plain := child.(*ast.Text); !plain {
				return ast.WalkContinue, nil
			}
		}
		for i := 0; i < paragraph.Lines().Len(); i++ {
			segment := physicalLine(paragraph.Lines().At(i))
			raw := string(segment.Value(source))
			trimmed := strings.TrimLeft(raw, " ")
			value, declaration := strings.CutPrefix(trimmed, "Depends-On:")
			if !declaration || len(raw)-len(trimmed) > 3 || len(strings.Fields(value)) != 1 {
				return ast.WalkContinue, nil
			}
		}
		for i := 0; i < paragraph.Lines().Len(); i++ {
			segment := physicalLine(paragraph.Lines().At(i))
			raw := segment.Value(source)
			accepted[segment.Start+len(raw)-len(bytes.TrimLeft(raw, " "))] = true
			lines = append(lines, segment)
		}
		return ast.WalkContinue, nil
	})
	line := 1
	for i := range source {
		if candidates[i] && !ignored[i] && !accepted[i] {
			return nil, fmt.Errorf("line %d: %w", line, ErrDeclarationAmbiguous)
		}
		if source[i] == '\n' {
			line++
		}
	}
	return lines, nil
}
