// Package condition parses and evaluates the Phase 1 condition grammar.
// Expressions are immutable after parsing and evaluation performs no I/O.
package condition

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type valueKind uint8

const (
	missingValue valueKind = iota
	stringValue
	listValue
	boolValue
)

// Value is a typed PR attribute. Its zero value represents missing data.
type Value struct {
	kind valueKind
	text string
	list []string
	flag bool
}

// String constructs a string attribute.
func String(s string) Value { return Value{kind: stringValue, text: s} }

// Strings constructs a list attribute, copying the caller's slice.
func Strings(s ...string) Value { return Value{kind: listValue, list: append([]string(nil), s...)} }

// Bool constructs a boolean attribute.
func Bool(b bool) Value { return Value{kind: boolValue, flag: b} }

// PR supplies a consistent snapshot of the attributes used by an expression.
type PR interface {
	Attribute(name string) Value
}

// Snapshot is a convenient in-memory implementation of PR.
type Snapshot map[string]Value

// Attribute returns a value, or a missing Value if the name is absent.
func (s Snapshot) Attribute(name string) Value { return s[name] }

var attributes = map[string]valueKind{
	"base": stringValue, "head": stringValue, "author": stringValue, "title": stringValue,
	"label": listValue, "draft": boolValue, "conflict": boolValue,
	"approved-reviews-by": listValue, "files": listValue,
	"check-success": listValue, "check-failure": listValue, "check-pending": listValue,
}

// Keep suggestion candidates ordered so equal distances have stable results.
var attributeNames = []string{
	"base", "head", "author", "title", "label", "draft", "conflict",
	"approved-reviews-by", "files", "check-success", "check-failure", "check-pending",
}

var operators = []string{"=", "!=", "~=", "glob", "<", "<=", ">", ">="}
var attributePrefix = regexp.MustCompile(`(?s)^(-?)(#?)([a-z][a-z0-9-]*)(.*)$`)

type expressionKind uint8

const (
	all expressionKind = iota
	anyOf
	not
	atom
)

// Expression is a compiled condition or logical group. The zero value is true.
type Expression struct {
	kind     expressionKind
	children []Expression
	name     string
	op       string
	value    string
	negative bool
	length   bool
	number   int
	pattern  *regexp.Regexp
}

// All combines expressions with AND. All() is true.
func All(children ...Expression) Expression {
	return Expression{kind: all, children: append([]Expression(nil), children...)}
}

// Any combines expressions with OR. Any() is false.
func Any(children ...Expression) Expression {
	return Expression{kind: anyOf, children: append([]Expression(nil), children...)}
}

// Not negates a single expression.
func Not(child Expression) Expression { return Expression{kind: not, children: []Expression{child}} }

// Parse compiles a single [-][#]attribute [operator value] condition.
// Values are the remainder of the string, without another layer of quoting.
func Parse(text string) (Expression, error) {
	m := attributePrefix.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return Expression{}, fmt.Errorf("expected [-][#]attribute [operator value]")
	}
	e := Expression{kind: atom, negative: m[1] != "", length: m[2] != "", name: m[3]}
	typ, ok := attributes[e.name]
	if !ok {
		return Expression{}, fmt.Errorf("unknown attribute %q; did you mean %q?", e.name, Closest(e.name, attributeNames))
	}
	if e.length && typ != listValue {
		return Expression{}, fmt.Errorf("#%s requires a list attribute", e.name)
	}
	rest := strings.TrimSpace(m[4])
	if rest == "" {
		if typ != boolValue || e.length {
			return Expression{}, fmt.Errorf("%s requires an operator and value", e.name)
		}
		return e, nil
	}
	end := strings.IndexAny(rest, " \t\n\r")
	if strings.ContainsRune("=!~<>", rune(rest[0])) {
		end = 0
		for end < len(rest) && strings.ContainsRune("=!~<>", rune(rest[end])) {
			end++
		}
	} else if end < 0 {
		end = len(rest)
	}
	e.op, e.value = rest[:end], strings.TrimSpace(rest[end:])
	if !contains(operators, e.op) {
		return Expression{}, fmt.Errorf("unknown operator %q; did you mean %q?", e.op, Closest(e.op, operators))
	}
	if e.value == "" {
		return Expression{}, fmt.Errorf("operator %q requires a value", e.op)
	}
	if e.length {
		if e.op == "~=" || e.op == "glob" {
			return Expression{}, fmt.Errorf("#%s requires a numeric comparison", e.name)
		}
		n, err := strconv.Atoi(e.value)
		if err != nil {
			return Expression{}, fmt.Errorf("#%s requires an integer, got %q", e.name, e.value)
		}
		e.number = n
	} else if typ == boolValue {
		if (e.op != "=" && e.op != "!=") || (e.value != "true" && e.value != "false") {
			return Expression{}, fmt.Errorf("%s requires = or != and a boolean (true or false)", e.name)
		}
	} else {
		switch e.op {
		case "=", "!=":
		case "~=", "glob":
			pattern := e.value
			var err error
			if e.op == "glob" {
				pattern, err = globPattern(pattern)
			}
			if err == nil {
				e.pattern, err = regexp.Compile(pattern)
			}
			if err != nil {
				return Expression{}, fmt.Errorf("invalid %s pattern %q: %w", e.op, e.value, err)
			}
		default:
			return Expression{}, fmt.Errorf("%s requires a string operator (=, !=, ~=, glob); use #%s for length comparisons", e.name, e.name)
		}
	}
	return e, nil
}

// String returns a stable, human-readable expression for failure reports.
func (e Expression) String() string {
	if e.kind != atom {
		parts := make([]string, len(e.children))
		for i, child := range e.children {
			parts[i] = child.String()
		}
		name := map[expressionKind]string{all: "and", anyOf: "or", not: "not"}[e.kind]
		return name + " (" + strings.Join(parts, "; ") + ")"
	}
	prefix := ""
	if e.negative {
		prefix += "-"
	}
	if e.length {
		prefix += "#"
	}
	text := prefix + e.name
	if e.op != "" {
		text += " " + e.op + " " + e.value
	}
	return text
}

// Evaluate returns whether the expression holds and the failed conditions in
// source order. AND reports its failed children; OR and NOT report the whole
// group, preserving their meaning. Success always returns a nil failure list.
// Missing or mistyped attributes fail closed, including negated atoms.
func (e Expression) Evaluate(pr PR) (bool, []string) {
	switch e.kind {
	case all:
		var failures []string
		for _, child := range e.children {
			_, failed := child.Evaluate(pr)
			failures = append(failures, failed...)
		}
		return len(failures) == 0, failures
	case anyOf:
		for _, child := range e.children {
			if ok, _ := child.Evaluate(pr); ok {
				return true, nil
			}
		}
	case not:
		if ok, _ := e.children[0].Evaluate(pr); !ok {
			return true, nil
		}
	case atom:
		if pr != nil {
			v := pr.Attribute(e.name)
			if v.kind == attributes[e.name] && e.matches(v) != e.negative {
				return true, nil
			}
		}
	}
	return false, []string{e.String()}
}

func (e Expression) matches(v Value) bool {
	if e.length {
		n := len(v.list)
		switch e.op {
		case "=":
			return n == e.number
		case "!=":
			return n != e.number
		case "<":
			return n < e.number
		case "<=":
			return n <= e.number
		case ">":
			return n > e.number
		case ">=":
			return n >= e.number
		}
	}
	if v.kind == boolValue {
		if e.op == "" {
			return v.flag
		}
		return (v.flag == (e.value == "true")) != (e.op == "!=")
	}
	values := v.list
	if v.kind == stringValue {
		values = []string{v.text}
	}
	matched := false
	for _, value := range values {
		if e.pattern != nil {
			matched = e.pattern.MatchString(value)
		} else {
			matched = value == e.value
		}
		if matched {
			break
		}
	}
	return matched != (e.op == "!=")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
