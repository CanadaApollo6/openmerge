package brief

import (
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"testing"
)

func TestAuthorityBriefWithoutApprovals(t *testing.T) {
	in := cp.Input{Classification: *sample().Classification, Current: receiptBasis(sample())}
	in.Classification.Facts["policy"] = cp.False
	in.Classification.Files[0].Facts["policy"] = cp.False
	p := cp.Policy{RequirePolicyOwners: true}
	ordinary, err := Build(p, in, "Title", "Body")
	if err != nil || !ordinary.ApprovalsRequired || len(ordinary.Obligations) != 0 {
		t.Fatalf("ordinary floor: %+v %v", ordinary, err)
	}
	in.Classification.Facts["policy"] = cp.True
	in.Classification.Files[0].Facts["policy"] = cp.True
	in.Classification.PolicyOwners = []cp.PolicyOwner{{Path: "a", Eligible: cp.IdentitySet{IDs: []string{"user"}, Resolved: true, Fresh: true}}}
	policy, err := Build(p, in, "Title", "Body")
	if err != nil || len(policy.Obligations) != 1 || policy.Obligations[0].Cause != cp.CausePolicyOwner {
		t.Fatalf("missing owner brief: %+v %v", policy, err)
	}
	if ordinaryDigest, err := Digest(ordinary); err != nil {
		t.Fatal(err)
	} else if policyDigest, err := Digest(policy); err != nil || ordinaryDigest == policyDigest {
		t.Fatal("owner obligation absent from brief digest", err)
	}
	in.Current.Head = "stale"
	if _, err := Build(p, in, "Title", "Body"); err == nil {
		t.Fatal("stale authority brief accepted")
	}
}
