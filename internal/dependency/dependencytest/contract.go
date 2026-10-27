// Package dependencytest supplies the same scoped-observation contract to fake
// and GitHub HTTP fixtures. It is test support, never runtime wiring.
package dependencytest

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// Env controls one isolated installation without real forge calls. GrantUser
// can deliberately differ from the declarer to model a reassigned login.
type Env struct {
	Reader      forge.DependencyReader
	SetGrant    func(forge.UserIdentity, string)
	SetSource   func(forge.DependencySource)
	SourceReads func() int
	FailSource  func()
	FailGrant   func()
}

// Request supplies a valid journal-scoped bound edge and B writer admission.
// All data is synthetic; no App or outside repository is consulted.
func Request() dependency.ObservationRequest {
	var token [32]byte
	token[0] = 1
	init, err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a", 64), token, "github:https://github.com", "7")
	if err != nil {
		panic(err)
	}
	scope := dependency.Scope{ForgeInstance: init.ForgeInstance, Installation: init.Installation, JournalNamespace: init.Namespace}
	ref := dependency.Reference{Scope: scope, ChangeID: "chg1_" + strings.Repeat("1", 64)}
	raw, err := ref.Canonical()
	if err != nil {
		panic(err)
	}
	base := dependency.Baseline{Revision: 7, IntentDigest: strings.Repeat("a", 64), Head: strings.Repeat("b", 40)}
	queue := dependency.QueueKey{Repository: "https://github.com/acme/project", Base: "main"}
	edge := dependency.DeclaredEdge{Reference: raw, Queue: queue, Baseline: base}
	alias := dependency.SourceAlias{ForgeInstance: init.ForgeInstance, Installation: init.Installation, RepositoryID: "60", Number: 2}
	consumer := dependency.DependenciesDeclared{
		Namespace: init.Namespace, Alias: alias, ChangeID: "chg1_" + strings.Repeat("2", 64), Revision: 1,
		Queue: dependency.QueueKey{Repository: "https://github.com/acme/client", Base: "main"}, Poll: 1,
		Edges: []dependency.DeclaredEdge{edge}, Bindings: []dependency.AliasBinding{}, Hold: dependency.DeclarationHold{Editors: []string{}},
		Attribution: dependency.Attribution{Editors: []string{"11"}}, ReadGrant: dependency.Grant{SubjectID: "11", ReturnedID: "11", RepositoryID: "60", ReadID: "body-read", Fresh: true, Allowed: true},
		Accountability: dependency.Accountability{Author: "11", Humans: []string{}},
	}
	consumer.Digest = dependency.DeclarationDigest(consumer.Edges)
	return dependency.ObservationRequest{
		Stage: dependency.BindingRead, Admission: dependency.QueueAdmission{Enqueued: true, CommandID: 1, ChangeID: consumer.ChangeID, Queue: consumer.Queue, Writer: dependency.Grant{SubjectID: "22", ReturnedID: "22", RepositoryID: "60", ReadID: "writer-command", Fresh: true, Allowed: true}},
		Registry: dependency.Registry{Scope: scope, Organization: "acme", Repositories: []string{queue.Repository, consumer.Queue.Repository}, Bindings: []dependency.Binding{{Reference: ref, Queue: queue}}},
		Consumer: consumer, Declaration: dependency.Basis{EventID: 3, Digest: consumer.Digest}, Edge: edge,
		Source:   dependency.SourceRecord{Alias: dependency.SourceAlias{ForgeInstance: init.ForgeInstance, Installation: init.Installation, RepositoryID: "50", Number: 1}, Reference: ref, Queue: queue, Current: base, BaseSHA: strings.Repeat("c", 40)},
		Declarer: forge.UserIdentity{ID: 11, Login: "alice", Kind: forge.UserAccount}, ReadID: "scoped-read-1", Sequence: 1,
	}
}

// Source supplies the complete forge view corresponding to Request's journal.
func Source(r dependency.ObservationRequest) forge.DependencySource {
	return forge.DependencySource{RepositoryID: r.Source.Alias.RepositoryID, Number: int(r.Source.Alias.Number), HeadSHA: r.Source.Current.Head, Base: r.Source.Queue.Base, BaseSHA: r.Source.BaseSHA, Body: "source declaration"}
}

// Run tests read-level permissions and confidential lifecycle behavior through
// the real observer using interchangeable fake and HTTP fixture drivers.
func Run(t *testing.T, newEnv func(*testing.T) Env) {
	setup := func(t *testing.T) (Env, dependency.ObservationRequest) {
		e, r := newEnv(t), Request()
		e.SetGrant(r.Declarer, "read")
		e.SetSource(Source(r))
		return e, r
	}
	read := func(t *testing.T, e Env, r dependency.ObservationRequest, want string) dependency.ScopedObservation {
		t.Helper()
		o, err := dependency.ObserveEdge(t.Context(), e.Reader, r)
		if err != nil || o.Evidence().Code != want {
			t.Fatalf("observation=%+v err=%v want=%s", o.Evidence(), err, want)
		}
		return o
	}
	for _, permission := range []string{"read", "triage", "write", "maintain", "admin", "none", "", "owner", "READ"} {
		t.Run("permission-"+permission, func(t *testing.T) {
			e, r := setup(t)
			e.SetGrant(r.Declarer, permission)
			allowed := permission == "read" || permission == "triage" || permission == "write" || permission == "maintain" || permission == "admin"
			want := "dependency-unavailable"
			if allowed {
				want = "dependency-waiting"
			}
			o := read(t, e, r, want)
			if (e.SourceReads() == 1) != allowed || o.Evidence().Grant.Allowed != allowed {
				t.Fatal("lifecycle read before declarer permission")
			}
		})
	}
	t.Run("identity-mismatch", func(t *testing.T) {
		e, r := setup(t)
		impostor := r.Declarer
		impostor.ID = 22
		e.SetGrant(impostor, "admin")
		read(t, e, r, "dependency-unavailable")
		if e.SourceReads() != 0 {
			t.Fatal("B writer/renamed login substituted for declarer")
		}
	})
	t.Run("fresh-revocation-at-every-stage", func(t *testing.T) {
		e, r := setup(t)
		for _, stage := range []dependency.ReadStage{dependency.BindingRead, dependency.WorkRead, dependency.LandingStartRead} {
			r.Stage = stage
			e.SetGrant(r.Declarer, "read")
			read(t, e, r, "dependency-waiting")
			before := e.SourceReads()
			e.SetGrant(r.Declarer, "none")
			read(t, e, r, "dependency-unavailable")
			if before != e.SourceReads() {
				t.Fatal("revoked grant reused at", stage)
			}
		}
	})
	for _, kind := range []string{"unknown-editor", "unauthorized-command", "not-enqueued", "no-command", "writer-id-mismatch", "writer-wrong-repository", "wrong-change", "wrong-queue", "substituted-declarer"} {
		t.Run(kind, func(t *testing.T) {
			e, r := setup(t)
			switch kind {
			case "unknown-editor":
				r.Consumer.Attribution.Unknown = true
			case "unauthorized-command":
				r.Admission.Writer.Allowed = false
			case "not-enqueued":
				r.Admission.Enqueued = false
			case "no-command":
				r.Admission.CommandID = 0
			case "writer-id-mismatch":
				r.Admission.Writer.ReturnedID = "11"
			case "writer-wrong-repository":
				r.Admission.Writer.RepositoryID = "50"
			case "wrong-change":
				r.Admission.ChangeID = "chg1_" + strings.Repeat("9", 64)
			case "wrong-queue":
				r.Admission.Queue.Base = "release"
			case "substituted-declarer":
				r.Declarer.ID = 22
			}
			if _, err := dependency.ObserveEdge(t.Context(), e.Reader, r); err == nil || e.SourceReads() != 0 {
				t.Fatal("graph discovery admitted before writer command/declarer authentication", err)
			}
		})
	}
	for _, kind := range []string{"unknown", "revisionless-push", "closed", "merged", "retarget-base", "retarget-change", "repository-id", "partial-head", "partial-base", "unserved", "foreign-scope", "stale-pin", "landed", "rewound", "stale-ancestry", "no-settlement", "failed-grant", "failed-source"} {
		t.Run(kind, func(t *testing.T) {
			e, r := setup(t)
			source := Source(r)
			want := "dependency-unavailable"
			switch kind {
			case "unknown":
				r.Source.Reference.ChangeID = ""
				want = "dependency-unresolved"
			case "revisionless-push":
				r.Source.Current.Revision = 8
				r.Source.Current.Head = strings.Repeat("d", 40)
				source.HeadSHA = r.Source.Current.Head
				want = "dependency-waiting"
			case "closed":
				source.Closed = true
				want = "dependency-closed"
			case "merged":
				source.Closed = true
				source.Merged = true
				want = "dependency-unverified"
			case "retarget-base":
				source.Base = "release"
				want = "dependency-source-retargeted"
			case "retarget-change":
				r.Source.Reference.ChangeID = "chg1_" + strings.Repeat("9", 64)
				want = "dependency-source-retargeted"
			case "repository-id":
				source.RepositoryID = "99"
				want = "dependency-unresolved"
			case "partial-head":
				source.HeadSHA = ""
			case "partial-base":
				source.BaseSHA = ""
			case "unserved":
				r.Registry.Repositories = r.Registry.Repositories[1:]
				want = "dependency-out-of-scope"
			case "foreign-scope":
				r.Source.Alias.Installation = "8"
				want = "dependency-out-of-scope"
			case "stale-pin":
				ref := r.Source.Reference
				ref.Revision = 7
				r.Edge.Reference, _ = ref.Canonical()
				r.Edge.PinnedHead = r.Edge.Baseline.Head
				r.Consumer.Edges = []dependency.DeclaredEdge{r.Edge}
				r.Consumer.Digest = dependency.DeclarationDigest(r.Consumer.Edges)
				r.Declaration.Digest = r.Consumer.Digest
				r.Source.Current.Revision = 8
				r.Source.Current.Head = strings.Repeat("d", 40)
				source.HeadSHA = r.Source.Current.Head
				want = "dependency-stale"
			case "landed", "rewound", "stale-ancestry", "no-settlement":
				source.Closed = true
				source.Merged = true
				r.Source.Landing = &dependency.SourceLanding{Revision: r.Source.Current, Commit: strings.Repeat("e", 40), SettlementEventID: 9, BaseSHA: r.Source.BaseSHA, OnBase: true}
				want = "dependency-landed"
				if kind == "rewound" {
					r.Source.Landing.OnBase = false
					want = "dependency-not-on-base"
				}
				if kind == "stale-ancestry" {
					r.Source.Landing.BaseSHA = strings.Repeat("f", 40)
					want = "dependency-unavailable"
				}
				if kind == "no-settlement" {
					r.Source.Landing.SettlementEventID = 0
					want = "dependency-unavailable"
				}
			case "failed-grant":
				e.FailGrant()
			case "failed-source":
				e.FailSource()
			}
			e.SetSource(source)
			o := read(t, e, r, want)
			generic := `{"reason":"Dependency cannot be verified"}`
			// Both the default serialization and every unauthorized/mixed audience
			// are byte-identical across existence, closure, supersession and success.
			if fmt.Sprintf("%+v", o) != "Dependency cannot be verified" || fmt.Sprintf("%#v", o) != "Dependency cannot be verified" {
				t.Fatal("default formatting leaked source state")
			}
			raw, err := json.Marshal(o)
			if err != nil || string(raw) != generic {
				t.Fatal("default serialization leaked", string(raw), err)
			}
			authorized := dependency.Grant{SubjectID: "33", ReturnedID: "33", RepositoryID: "50", ReadID: "audience-read", Fresh: true, Allowed: true}
			denied := authorized
			denied.Allowed = false
			unknown := authorized
			unknown.Fresh = false
			mismatch := authorized
			mismatch.ReturnedID = "44"
			foreign := authorized
			foreign.RepositoryID = "60"
			for _, audience := range [][]dependency.Grant{nil, {denied}, {unknown}, {mismatch}, {foreign}, {authorized, denied}} {
				raw, err := json.Marshal(o.View(audience))
				if err != nil || string(raw) != generic {
					t.Fatal("audience leak", string(raw), err)
				}
			}
			if kind != "unserved" && kind != "foreign-scope" && kind != "failed-grant" {
				if view := o.View([]dependency.Grant{authorized}); view.Code != want || view.Observation == nil {
					t.Fatal("authorized detail unavailable", view)
				}
			}
			if (kind == "unserved" || kind == "foreign-scope" || kind == "failed-grant") && e.SourceReads() != 0 {
				t.Fatal("out-of-scope/unauthorized lifecycle read")
			}
		})
	}
}
