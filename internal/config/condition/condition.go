// Package condition parses and evaluates the Phase 1 condition grammar.
// Expressions are immutable after parsing and evaluation performs no I/O.
package condition

import (
	"fmt"
	"regexp"
	"slices"
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
	kind      valueKind
	text      string
	list      []string
	flag      bool
	checks    bool
	observed  []string
	unsettled []string
}

// String constructs a string attribute.
func String(s string) Value { return Value{kind: stringValue, text: s} }

// Strings constructs a list attribute, copying the caller's slice.
func Strings(s ...string) Value { return Value{kind: listValue, list: append([]string(nil), s...)} }

// CheckResults builds CI attributes with per-name pending/missing knowledge.
// Pending runs may still join either terminal list. Missing named checks are
// unknown, so negation cannot authorize a batch before its CI appears.
func CheckResults(success, failure, pending []string) Snapshot {
	observed := append(append(append([]string{}, success...), failure...), pending...)
	result := Snapshot{}
	for name, list := range map[string][]string{"check-success": success, "check-failure": failure, "check-pending": pending} {
		value := Strings(list...)
		value.checks, value.observed = true, observed
		if name != "check-pending" {
			value.unsettled = append([]string{}, pending...)
		}
		result[name] = value
	}
	return result
}

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

// Registry order breaks spelling-suggestion ties deterministically.
var attributes = []struct {
	name string
	kind valueKind
}{
	{"base", stringValue}, {"head", stringValue}, {"author", stringValue}, {"title", stringValue},
	{"label", listValue}, {"draft", boolValue}, {"conflict", boolValue},
	{"approved-reviews-by", listValue}, {"files", listValue},
	{"check-success", listValue}, {"check-failure", listValue}, {"check-pending", listValue},
}

var operators = []string{"=", "!=", "~=", "glob", "<", "<=", ">", ">="}
var attributePrefix = regexp.MustCompile(`(?s)^(-?)(#?)([A-Za-z_][A-Za-z0-9_-]*)(.*)$`)

func attributeKind(name string) valueKind {
	for _, attribute := range attributes {
		if attribute.name == name {
			return attribute.kind
		}
	}
	return missingValue
}

func attributeNames() []string {
	names := make([]string, len(attributes))
	for i, attribute := range attributes {
		names[i] = attribute.name
	}
	return names
}

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
	typ := attributeKind(e.name)
	if typ == missingValue {
		return Expression{}, fmt.Errorf("unknown attribute %q%s", e.name, Suggestion(e.name, attributeNames()))
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
		return Expression{}, fmt.Errorf("unknown operator %q%s; valid operators: %s", e.op, Suggestion(e.op, operators), strings.Join(operators, ", "))
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
		value := e.value
		if !e.length && attributeKind(e.name) != boolValue {
			value = strconv.Quote(value)
		}
		text += " " + e.op + " " + value
	}
	return text
}

// Attributes returns a sorted, unique copy of every referenced attribute name,
// including attributes under NOT and in alternatives that might short-circuit.
func (e Expression) Attributes() []string {
	seen := make(map[string]bool)
	var visit func(Expression)
	visit = func(expression Expression) {
		if expression.kind == atom {
			seen[expression.name] = true
		}
		for _, child := range expression.children {
			visit(child)
		}
	}
	visit(e)
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Failure describes a condition that did not pass. Actual is a string, bool,
// []string, list length (int), or nil for missing data and logical groups.
// Children retain a group's evidence, including met operands under failed NOT.
// Unknown is the result of this condition; child evidence can still be unknown
// when an AND is definitively not met. Missing distinguishes absent from mistyped.
type Failure struct {
	Condition string    `json:"condition"`
	Attribute string    `json:"attribute,omitempty"`
	Actual    any       `json:"actual"`
	Missing   bool      `json:"missing"`
	Unknown   bool      `json:"unknown"`
	Children  []Failure `json:"children,omitempty"`
}

// String returns the normalized expression, with string operands quoted.
func (f Failure) String() string { return f.Condition }

type result uint8

const (
	notMet result = iota
	met
	unknown
)

type evaluation struct {
	result   result
	evidence Failure
	failures []Failure
}

// Evaluate admits only met conditions. Missing or mistyped data is unknown,
// which never passes, even under NOT or the '-' prefix. AND collects failed
// children in source order; failed OR/NOT groups retain their operand evidence.
// Success always returns nil failures.
func (e Expression) Evaluate(pr PR) (bool, []Failure) {
	evaluated := e.evaluate(pr)
	return evaluated.result == met, evaluated.failures
}

func (e Expression) evaluate(pr PR) evaluation {
	evaluated := evaluation{evidence: Failure{Condition: e.String()}}
	switch e.kind {
	case all, anyOf:
		evaluated.result = met
		if e.kind == anyOf {
			evaluated.result = notMet
		}
		for _, child := range e.children {
			value := child.evaluate(pr)
			evaluated.evidence.Children = append(evaluated.evidence.Children, value.evidence)
			if e.kind == all {
				evaluated.failures = append(evaluated.failures, value.failures...)
				if value.result == notMet || evaluated.result == met {
					evaluated.result = value.result
				}
			} else if value.result == met || evaluated.result == notMet {
				evaluated.result = value.result
			}
		}
	case not:
		child := e.children[0].evaluate(pr)
		evaluated.evidence.Children = []Failure{child.evidence}
		evaluated.result = unknown
		switch child.result {
		case met:
			evaluated.result = notMet
		case notMet:
			evaluated.result = met
		}
	case atom:
		var value Value
		if pr != nil {
			value = pr.Attribute(e.name)
		}
		evaluated.evidence.Attribute = e.name
		evaluated.evidence.Missing = value.kind == missingValue
		switch value.kind {
		case stringValue:
			evaluated.evidence.Actual = value.text
		case boolValue:
			evaluated.evidence.Actual = value.flag
		case listValue:
			evaluated.evidence.Actual = append([]string{}, value.list...)
			if e.length {
				evaluated.evidence.Actual = len(value.list)
			}
		}
		if value.kind != attributeKind(e.name) || value.checks && e.checkUnknown(value) {
			evaluated.result = unknown
		} else if e.matches(value) != e.negative {
			evaluated.result = met
		}
	}
	evaluated.evidence.Unknown = evaluated.result == unknown
	if evaluated.result != met && e.kind != all {
		evaluated.failures = []Failure{evaluated.evidence}
	}
	return evaluated
}

func (e Expression) checkUnknown(value Value) bool {
	if !slices.ContainsFunc(value.observed, e.ReferencesCheck) {
		return true
	}
	matched := e.matches(value)
	possible := Strings(value.list...)
	for _, name := range value.unsettled {
		if !e.length && !e.ReferencesCheck(name) {
			continue
		}
		possible.list = append(possible.list, name)
		if e.matches(possible) != matched {
			return true
		}
	}
	return false
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

// ReferencesCheck reports whether a named producer can affect a check predicate.
// Counts and exclusions inspect the entire list; equality/pattern predicates
// inspect only matching names. This also visits predicates beneath NOT and OR.
func (e Expression) ReferencesCheck(name string) bool {
	if e.kind == atom && strings.HasPrefix(e.name, "check-") {
		return e.length || e.op == "!=" || e.matches(Strings(name))
	}
	for _, child := range e.children {
		if child.ReferencesCheck(name) {
			return true
		}
	}
	return false
}
