package dependency

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

func sortReferences(refs []Reference) {
	slices.SortFunc(refs, func(a, b Reference) int { return strings.Compare(a.ChangeID, b.ChangeID) })
}

// ParseIntent reads only standalone source lines in top-level CommonMark
// paragraphs of the authoritative PR body. Lists, headings, code, quotes, HTML
// blocks and lines touching inline code or raw HTML carry no declarations.
// GFM tables are enabled to exclude table cells; other GFM extensions do not
// change these block boundaries. Exact, case-sensitive source text is used,
// with CRLF normalized and up to three leading spaces allowed. A malformed
// recognized line returns no partial result. Commits/comments are never scanned.
func ParseIntent(body string, scope Scope) ([]Input, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	source := []byte(strings.ReplaceAll(body, "\r\n", "\n"))
	var out []Input
	for _, segment := range intentLines(source) {
		line := string(segment.Value(source))
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 {
			continue
		}
		value, recognized := strings.CutPrefix(trimmed, "Depends-On:")
		if !recognized {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) != 1 {
			return nil, fmt.Errorf("line %d: %w", bytes.Count(source[:segment.Start], []byte{'\n'})+1, ErrMalformed)
		}
		input, err := parseInput(fields[0], scope)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", bytes.Count(source[:segment.Start], []byte{'\n'})+1, err)
		}
		out = append(out, input)
	}
	return out, nil
}

// intentLines preserves whole physical lines rather than rendering inline text:
// unescaping or removing markup must never synthesize a declaration. Inline
// code and raw HTML can span multiple paragraph lines, so exclude every line
// overlapping their source segments. AST/source order makes the scan linear.
func intentLines(source []byte) []text.Segment {
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(source))
	var lines []text.Segment
	for block := doc.FirstChild(); block != nil; block = block.NextSibling() {
		paragraph, ok := block.(*ast.Paragraph)
		if !ok {
			continue
		}
		var shielded []text.Segment
		// This visitor cannot fail: it only collects source segments.
		_ = ast.Walk(paragraph, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch node := node.(type) {
			case *ast.CodeSpan:
				for child := node.FirstChild(); child != nil; child = child.NextSibling() {
					shielded = append(shielded, child.(*ast.Text).Segment)
				}
				return ast.WalkSkipChildren, nil
			case *ast.RawHTML:
				for i := 0; i < node.Segments.Len(); i++ {
					shielded = append(shielded, node.Segments.At(i))
				}
			}
			return ast.WalkContinue, nil
		})
		next := 0
		for i := 0; i < paragraph.Lines().Len(); i++ {
			segment := paragraph.Lines().At(i)
			start := bytes.LastIndexByte(source[:segment.Start], '\n') + 1
			stop := len(source)
			if end := bytes.IndexByte(source[segment.Start:], '\n'); end >= 0 {
				stop = segment.Start + end + 1
			}
			for next < len(shielded) && shielded[next].Stop <= start {
				next++
			}
			if next < len(shielded) && shielded[next].Start < stop {
				continue
			}
			lines = append(lines, text.NewSegment(start, stop))
		}
	}
	return lines
}
