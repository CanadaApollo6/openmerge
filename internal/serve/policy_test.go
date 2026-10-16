package serve

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestCheckPolicyKeepsPendingRerunsAndAmbiguity(t *testing.T) {
	expression, err := condition.Parse("check-success = ci")
	must(t, err)
	pr := forge.PullRequest{Base: "main"}
	checks := []forge.CheckRun{{ID: 1, Name: "ci", Source: "app:1", State: forge.Failure}, {ID: 2, Name: "ci", Source: "app:1", State: forge.Pending}}
	met, err := evaluate(expression, pr, checks)
	must(t, err)
	if met || failedCheck(expression, checks) {
		t.Fatal("stale failure overrode pending rerun")
	}
	checks[1].State = forge.Success
	met, err = evaluate(expression, pr, checks)
	must(t, err)
	if !met {
		t.Fatal("passing rerun did not supersede failure")
	}
	checks = append(checks, forge.CheckRun{ID: 3, Name: "unrequired", Source: "app:1", State: forge.Success}, forge.CheckRun{ID: 4, Name: "unrequired", Source: "app:2", State: forge.Success})
	met, err = evaluate(expression, pr, checks)
	must(t, err)
	if !met {
		t.Fatal("unrequired producer ambiguity blocked CI")
	}
	checks = append(checks, forge.CheckRun{ID: 5, Name: "ci", Source: "app:2", State: forge.Success})
	_, err = evaluate(condition.Not(expression), pr, checks)
	var ambiguity *executor.CheckSourceAmbiguityError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("negation hid producer ambiguity: %v", err)
	}
}
