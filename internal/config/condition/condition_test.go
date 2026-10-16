package condition

import (
	"reflect"
	"strings"
	"testing"
)

func TestEvaluate(t *testing.T) {
	pr := Snapshot{
		"base": String("main"), "head": String("feature/config"),
		"author": String("riel"), "title": String("Add config validation"),
		"label": Strings("ready", "backend"), "draft": Bool(false), "conflict": Bool(true),
		"approved-reviews-by": Strings("alice", "bob"), "files": Strings("main.go", "internal/config/load.go", "docs/a.md"),
		"check-success": Strings("lint", "unit"), "check-failure": Strings("integration"), "check-pending": Strings("security"),
	}
	tests := []struct {
		text string
		want bool
	}{
		{"base = main", true}, {"base=main", true}, {"base != main", false}, {"base != release", true},
		{"head ~= ^feature/", true}, {"head glob feature/*", true}, {"head glob fix/*", false},
		{"author = riel", true}, {"author = alice", false}, {"title = Add config validation", true},
		{"title ~= config", true}, {"title ~= ^config", false},
		{"label = ready", true}, {"label = missing", false}, {"label != ready", false}, {"label != missing", true},
		{"label ~= ^back", true}, {"label glob back*", true},
		{"draft", false}, {"-draft", true}, {"conflict", true}, {"-conflict", false},
		{"draft = false", true}, {"draft = true", false}, {"draft != false", false}, {"draft != true", true},
		{"conflict = true", true}, {"conflict != true", false}, {"-draft = false", false},
		{"approved-reviews-by = bob", true}, {"approved-reviews-by != bob", false},
		{"files = main.go", true}, {"files glob **/*.go", true}, {"files glob *.go", true},
		{"check-success = lint", true}, {"check-failure = integration", true}, {"check-pending = security", true},
		{"#label = 2", true}, {"#label = 1", false}, {"#label != 1", true}, {"#label != 2", false},
		{"#label < 3", true}, {"#label < 2", false}, {"#label <= 2", true}, {"#label <= 1", false},
		{"#label > 1", true}, {"#label > 2", false}, {"#label >= 2", true}, {"#label >= 3", false},
		{"#label > -1", true}, {"#label = -1", false},
		{"-#label >= 3", true}, {"-#label >= 2", false}, {"#approved-reviews-by >= 1", true},
		{"-label = missing", true}, {"-label != missing", false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()
			e, err := Parse(tt.text)
			if err != nil {
				t.Fatal(err)
			}
			got, failed := e.Evaluate(pr)
			if got != tt.want {
				t.Errorf("Evaluate = %v, want %v", got, tt.want)
			}
			var wantFailed []string
			if !tt.want {
				wantFailed = []string{e.String()}
			}
			if !reflect.DeepEqual(failed, wantFailed) {
				t.Errorf("failures = %q, want %q", failed, wantFailed)
			}
		})
	}
}

func TestAttributeTypes(t *testing.T) {
	for _, name := range attributeNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var valid string
			var invalid []string
			switch attributes[name] {
			case stringValue:
				valid = name + " = value"
				invalid = []string{name, "#" + name + " = 1", name + " > 3"}
			case listValue:
				valid = "#" + name + " >= 0"
				invalid = []string{name, name + " > 3", "#" + name + " ~= pattern"}
			case boolValue:
				valid = name
				invalid = []string{name + " = foo", "#" + name, name + " glob true", name + " > 3"}
			}
			if _, err := Parse(valid); err != nil {
				t.Errorf("Parse(%q): %s", valid, err)
			}
			for _, text := range invalid {
				if _, err := Parse(text); err == nil {
					t.Errorf("Parse(%q) accepted an invalid type", text)
				}
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ text, want string }{
		{"", "expected [-][#]attribute [operator value]"},
		{"--draft", "expected [-][#]attribute [operator value]"},
		{"#-draft", "expected [-][#]attribute [operator value]"},
		{"drfat", `unknown attribute "drfat"; did you mean "draft"?`},
		{"check-succes = ci", `unknown attribute "check-succes"; did you mean "check-success"?`},
		{"base glbo main", `unknown operator "glbo"; did you mean "glob"?`},
		{"base == main", `unknown operator "=="; did you mean "="?`},
		{"base ! main", `unknown operator "!"; did you mean "="?`},
		{"base =", `operator "=" requires a value`},
		{"#label = 1.5", `#label requires an integer, got "1.5"`},
		{"#label = many", `#label requires an integer, got "many"`},
		{"#label = 999999999999999999999", `#label requires an integer`},
		{"#label", `label requires an operator and value`},
		{"#label glob 3", `#label requires a numeric comparison`},
		{"draft = foo", `draft requires = or != and a boolean (true or false)`},
		{"title ~= [", `invalid ~= pattern "[": error parsing regexp`},
		{"title ~= (?=x)", `invalid ~= pattern "(?=x)": error parsing regexp`},
		{"files glob foo\\", `invalid glob pattern "foo\\": trailing backslash`},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestListSemantics(t *testing.T) {
	for _, name := range []string{"label", "approved-reviews-by", "files", "check-success", "check-failure", "check-pending"} {
		for _, values := range [][]string{nil, {}, {"wanted"}, {"other", "wanted"}, {"other"}} {
			for _, op := range []string{"=", "!=", "~=", "glob"} {
				text := name + " " + op + " wanted"
				e, err := Parse(text)
				if err != nil {
					t.Fatal(err)
				}
				want := contains(values, "wanted") != (op == "!=")
				if got, _ := e.Evaluate(Snapshot{name: Strings(values...)}); got != want {
					t.Errorf("%s on %q = %v, want %v", text, values, got, want)
				}
			}
		}
	}
}

func TestGlob(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "main.go", true}, {"*.go", "src/main.go", false},
		{"**/*.go", "main.go", true}, {"**/*.go", "src/nested/main.go", true},
		{"**/*.go", "src/main.go.txt", false}, {"**/*.go", "MAIN.GO", false},
		{"src/**/a?.go", "src/a1.go", true}, {"src/**/a?.go", "src/x/y/a1.go", true},
		{"src/**/a?.go", "src/a12.go", false}, {"src/?/a.go", "src/x/a.go", true},
		{"src/?/a.go", "src/xy/a.go", false}, {"src/?/a.go", "src///a.go", false},
		{"docs/**", "docs/a/b.md", true}, {"docs/**", "other/docs/a.md", false},
		{"**/*.go", "src/line\nbreak/main.go", true},
		{`a\*.go`, "a*.go", true}, {`a\?.go`, "a?.go", true}, {"a[1].go", "a[1].go", true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.path, func(t *testing.T) {
			t.Parallel()
			e, err := Parse("files glob " + tt.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := e.Evaluate(Snapshot{"files": Strings(tt.path)}); got != tt.want {
				t.Errorf("matched = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGroupsAndFailures(t *testing.T) {
	parse := func(s string) Expression {
		t.Helper()
		e, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	draft, conflict, ready := parse("draft"), parse("conflict"), parse("label = ready")
	pr := Snapshot{"draft": Bool(false), "conflict": Bool(false), "label": Strings("blocked")}
	tests := []struct {
		name string
		e    Expression
		want bool
		fail []string
	}{
		{"empty AND", All(), true, nil}, {"empty OR", Any(), false, []string{"or ()"}},
		{"AND", All(draft, conflict), false, []string{"draft", "conflict"}},
		{"OR", Any(draft, conflict), false, []string{"or (draft; conflict)"}},
		{"NOT succeeds", Not(draft), true, nil},
		{"NOT fails", Not(Not(draft)), false, []string{"not (not (draft))"}},
		{"OR suppresses failed alternatives", Any(draft, Not(conflict)), true, nil},
		{"nested failure", All(Not(draft), Any(conflict, ready)), false, []string{"or (conflict; label = ready)"}},
		{"nested AND", All(All(draft), ready), false, []string{"draft", "label = ready"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, failed := tt.e.Evaluate(pr)
			if got != tt.want || !reflect.DeepEqual(failed, tt.fail) {
				t.Fatalf("Evaluate = %v, %q; want %v, %q", got, failed, tt.want, tt.fail)
			}
		})
	}
}

func TestMissingAndMistypedAttributes(t *testing.T) {
	for _, text := range []string{"draft", "-draft", "base = main", "base != main", "#label = 0", "-label = ready"} {
		e, err := Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range []PR{nil, Snapshot{}, Snapshot{"draft": String("false"), "base": Bool(false), "label": Bool(false)}} {
			if got, failed := e.Evaluate(pr); got || len(failed) != 1 {
				t.Errorf("%s on %v = %v, %v; want false with one failure", text, pr, got, failed)
			}
		}
	}
}

func TestStableStringAndSnapshotCopy(t *testing.T) {
	e, err := Parse("  -#label>=2  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.String(); got != "-#label >= 2" {
		t.Fatalf("String = %q", got)
	}
	labels := []string{"ready"}
	v := Strings(labels...)
	labels[0] = "changed"
	ready, err := Parse("label = ready")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := ready.Evaluate(Snapshot{"label": v}); !ok {
		t.Fatal("Strings retained mutable caller data")
	}
}
