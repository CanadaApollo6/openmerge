package status

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
)

// TestDependencyWaitAgeAuthorizedOnly flags only an authorized, blocking wait
// at least the threshold old. The zero option keeps 5-1 output unchanged, and
// the flag never changes WaitingReason precedence or blocking.
func TestDependencyWaitAgeAuthorizedOnly(t *testing.T) {
	sources := statusfixture.StatusSources()
	for _, tc := range []struct {
		name, code string
		after      time.Duration
		authorized bool
		flagged    bool
	}{
		{"old authorized wait", "dependency-waiting", 2 * time.Hour, true, true},
		{"young authorized wait", "dependency-waiting", 30 * time.Minute, true, false},
		{"old unauthorized wait", "dependency-waiting", 2 * time.Hour, false, false},
		{"eligible", "dependency-landed", 2 * time.Hour, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, binding, ref, grant := statusfixture.StatusJournal(t, tc.code, 1)
			audience := map[string][]dependency.Grant{}
			if tc.authorized {
				audience[ref] = []dependency.Grant{grant}
			}
			legacy, err := ProjectDependencies(j, binding, audience, sources)
			if err != nil {
				t.Fatal(err)
			}
			at := j.Events[0].RecordedAt
			got, err := ProjectDependenciesAt(j, binding, audience, sources, DependencyOptions{Now: at.Add(tc.after), WaitAge: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			item, base := got.DependencyStatus[2], legacy.DependencyStatus[2]
			if !tc.flagged {
				if !reflect.DeepEqual(item, base) {
					t.Fatalf("unflagged view changed:\n%+v\n%+v", item, base)
				}
				return
			}
			if item.WaitAge == nil || !item.WaitAge.Since.Equal(at) || item.WaitAge.Threshold != time.Hour || !slices.Equal(item.Details[:len(base.Details)], base.Details) || item.Details[len(item.Details)-1] != "dependency wait older than 1h0m0s (non-actionable; for operator review)" || item.Blocking != base.Blocking || item.Text != base.Text {
				t.Fatalf("flag %+v", item)
			}
			_, e, _ := Project(got, 2, 20, nil)
			_, legacyEntry, _ := Project(legacy, 2, 20, nil)
			if e.Dependency.WaitAge == nil || e.Dependency.WaitAge.OlderThan != "1h0m0s" || e.WaitingReason.Code != legacyEntry.WaitingReason.Code || e.WaitingReason.Text != legacyEntry.WaitingReason.Text {
				t.Fatalf("presentation %+v / %+v", e.Dependency, e.WaitingReason)
			}
		})
	}
}

// statusViewFields pins every StatusView field so a new one must be classified.
var statusViewFields = []string{"Diagnostics", "Text", "Details", "URL", "Redacted", "Blocking", "SelfAcknowledgement", "Warnings"}

// TestDependencyItemReflectAllowlist fills a complete StatusView and requires
// the stored presentation to keep only allowlisted leaves. The journal event
// reference appears only while the queue's journal positions are public.
func TestDependencyItemReflectAllowlist(t *testing.T) {
	var names []string
	for i := range reflect.TypeFor[dependency.StatusView]().NumField() {
		names = append(names, reflect.TypeFor[dependency.StatusView]().Field(i).Name)
	}
	if !slices.Equal(names, statusViewFields) {
		t.Fatalf("StatusView fields %v must be classified for redaction", names)
	}
	var view dependency.StatusView
	reflectFill(t, reflect.ValueOf(&view).Elem(), "", map[reflect.Type]int{})
	allowed := []string{".Text", ".Details[]", ".URL", ".Warnings[]", ".Diagnostics[].Code", ".Diagnostics[].Source", ".SelfAcknowledgement.Kind", ".SelfAcknowledgement.Actor", ".SelfAcknowledgement.Overridden[]", ".SelfAcknowledgement.GoverningCommit"}
	for _, redacted := range []bool{true, false} {
		item := dependencyItem(view, redacted, nil)
		leaves := map[string]string{}
		reflectLeaves(reflect.ValueOf(item), "", leaves)
		for path, value := range leaves {
			source, filled := strings.CutPrefix(value, "filled:")
			switch {
			case path == ".SelfAcknowledgement.EventID":
				if redacted {
					t.Fatalf("journal reference %s survived redaction", value)
				}
			case filled && slices.Contains(allowed, source):
			case value == "true" && (path == ".Redacted" || path == ".Blocking" || path == ".SelfAcknowledgement.Revoked"):
			default:
				t.Fatalf("non-allowlisted leaf %s=%s", path, value)
			}
		}
		if _, ok := leaves[".SelfAcknowledgement.EventID"]; ok == redacted {
			t.Fatalf("event reference presence %t with redacted=%t", ok, redacted)
		}
	}
}
