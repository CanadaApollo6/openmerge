package queue

import (
	"fmt"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// RecordedBrief builds receipt identity exclusively from the journal projection.
// Fresh owner membership belongs to EvaluateApprovals and cannot enter this API.
func (s State) RecordedBrief(policy cp.Policy, b ApprovalBasis) (brief.Fact, error) {
	if err := s.CheckApprovalBoundary(b); err != nil {
		return brief.Fact{}, err
	}
	stored, ok := s.Classification(b.ChangeID, b.Revision)
	f, _ := s.ApprovalFacts(b.ChangeID)
	if !ok || f.Intent == nil || f.Intent.Basis.Revision != b.Revision || f.Intent.Basis.Head != b.Head || f.Accountability == nil || f.Accountability.Basis.Revision != b.Revision || f.Accountability.Basis.Head != b.Head {
		return brief.Fact{}, fmt.Errorf("brief evidence not recorded")
	}
	c := stored.Evidence.Classification
	i := c.Inputs
	if f.Accountability.PolicyDigest != i.PolicyDigest {
		return brief.Fact{}, fmt.Errorf("brief accountability policy mismatch")
	}
	in := cp.Input{Classification: c, AuthorKind: f.Accountability.Author.Kind, Current: cp.ReceiptIdentity{ChangeID: i.ChangeID, Revision: i.Revision, Generation: i.Generation, Base: i.Base, Head: i.Head, MergeBase: i.MergeBase, PolicyDigest: i.PolicyDigest}, Stances: map[string]cp.Stance{}}
	in.ExcludedIDs = append(in.ExcludedIDs, f.Accountability.Author.ID, f.Accountability.SubmitterID)
	for _, h := range f.Accountability.Humans {
		in.ExcludedIDs = append(in.ExcludedIDs, h.ID)
	}
	for user, key := range f.Latest {
		v := f.Sources[key]
		if v.Revision == b.Revision {
			in.Stances[user] = v.Event.Stance
		}
	}
	return brief.Build(policy, in, f.Intent.Title, f.Intent.Body)
}
