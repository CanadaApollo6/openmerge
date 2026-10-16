package condition

import "testing"

func TestCheckResultsThreeValued(t *testing.T) {
	linux, fallback := mustParse(t, "check-success = linux"), mustParse(t, "check-success = fallback")
	for _, tt := range []struct {
		name                      string
		expression                Expression
		success, failure, pending []string
		met, falseResult          bool
	}{
		{"OR pending", Any(linux, fallback), nil, []string{"linux"}, []string{"fallback"}, false, false},
		{"OR missing", Any(linux, fallback), nil, []string{"linux"}, nil, false, false},
		{"OR success", Any(linux, fallback), []string{"fallback"}, []string{"linux"}, nil, true, false},
		{"OR failed", Any(linux, fallback), nil, []string{"linux", "fallback"}, nil, false, true},
		{"AND failed", All(linux, fallback), nil, []string{"linux"}, []string{"fallback"}, false, true},
		{"NOT pending", Not(linux), nil, nil, []string{"linux"}, false, false},
		{"NOT missing", Not(linux), nil, nil, nil, false, false},
		{"NOT failed", Not(linux), nil, []string{"linux"}, nil, true, false},
		{"negative pending", mustParse(t, "-check-success = linux"), nil, nil, []string{"linux"}, false, false},
		{"exclusion pending", mustParse(t, "check-success != linux"), nil, nil, []string{"linux"}, false, false},
		{"count pending", mustParse(t, "#check-success >= 1"), nil, nil, []string{"linux"}, false, false},
		{"count sufficient", mustParse(t, "#check-success >= 1"), []string{"linux"}, nil, []string{"fallback"}, true, false},
		{"count intermediate", mustParse(t, "#check-success != 1"), nil, nil, []string{"linux", "fallback"}, false, false},
		{"pattern pending", mustParse(t, "check-success glob ci-*"), nil, []string{"ci-linux"}, []string{"ci-fallback"}, false, false},
		{"pattern failed", mustParse(t, "check-success glob ci-*"), nil, []string{"ci-linux", "ci-fallback"}, nil, false, true},
		{"explicit pending", mustParse(t, "check-pending = linux"), nil, nil, []string{"linux"}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			values := CheckResults(tt.success, tt.failure, tt.pending)
			met, _ := tt.expression.Evaluate(values)
			falseResult, _ := Not(tt.expression).Evaluate(values)
			if met != tt.met || falseResult != tt.falseResult {
				t.Fatalf("met/definitely false = %v/%v, want %v/%v", met, falseResult, tt.met, tt.falseResult)
			}
		})
	}
}
