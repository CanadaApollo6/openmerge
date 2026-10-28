package dependency

import (
 "reflect"
 "slices"
)

// CompositionMember freezes the dependency inputs consumed before build I/O.
// Observation IDs retain the original audit evidence; unchanged refreshes do
// not invalidate a composition merely because their journal IDs differ.
type CompositionMember struct {
 Alias SourceAlias `json:"alias"`
 ChangeID string `json:"change_id"`
 Revision uint64 `json:"revision"`
 Head string `json:"head"`
 Admission AdmissionBasis `json:"admission"`
 Observations []Observed `json:"observations"`
}

// CompositionRecorded binds the exact durable admission, before its builder
// publishes a ref. Existing batch payloads remain unchanged.
type CompositionRecorded struct {
 Namespace string `json:"journal_namespace"`
 BatchID string `json:"batch_id"`
 StartEventID uint64 `json:"start_event_id"`
 BaseSHA string `json:"base_sha"`
 Members []CompositionMember `json:"members"`
}
func (CompositionRecorded) dependencyEvent() {}
func (e CompositionRecorded) Validate() error {
 if !validDigestID(e.Namespace,"omj1_") || !cleanText(e.BatchID) || e.StartEventID == 0 || !objectID(e.BaseSHA) || len(e.Members)==0 || len(e.Members)>128 { return ErrMalformed }
 seen := map[string]bool{}
 for _, m := range e.Members {
  if !m.Alias.valid() || !validDigestID(m.ChangeID,"chg1_") || seen[m.ChangeID] || m.Revision==0 || !objectID(m.Head) || !m.Admission.valid() || m.Observations==nil || len(m.Observations)!=len(m.Admission.ObservationIDs) { return ErrMalformed }
  seen[m.ChangeID]=true
  for _, o := range m.Observations { if o.Validate()!=nil || o.Result!="landed" || o.Alias!=m.Alias || o.ChangeID!=m.ChangeID || o.Revision!=m.Revision || o.Namespace!=e.Namespace || o.Declaration!=m.Admission.Declaration { return ErrMalformed } }
 }
 return nil
}

type compositionProjection struct {
 EventID uint64 `json:"event_id"`
 Event CompositionRecorded `json:"event"`
}
func cloneComposition(e CompositionRecorded) CompositionRecorded {
 e.Members=slices.Clone(e.Members)
 for i := range e.Members { m:= &e.Members[i]; m.Admission.ObservationIDs=slices.Clone(m.Admission.ObservationIDs); m.Observations=slices.Clone(m.Observations) }
 return e
}

// CompositionMember derives a build input exclusively from recorded facts.
func (s State) CompositionMember(change string, revision uint64, head string) (CompositionMember,error) {
 a:=s.Admission(change,revision)
 if a.Code!="" { return CompositionMember{},ErrNotAdmitted }
 m:=CompositionMember{a.Alias,change,revision,head,a.Basis,[]Observed{}}
 edges,_,_:=s.Edges(change,revision)
 for _, edge:=range edges { o,_,_:=s.Observation(Observed{Alias:a.Alias,ChangeID:change,Revision:revision,Source:edge.Reference}); m.Observations=append(m.Observations,o) }
 return m,nil
}

// Composition returns a detached active companion and its immutable ID.
func (s State) Composition(batch string) (CompositionRecorded,uint64,bool) {
 c,ok:=s.compositions[batch]; return cloneComposition(c.Event),c.EventID,ok
}

// CompositionValid compares semantic dependency inputs. Even if a source later
// recovers, the first observed invalidation must retire the old composition;
// the reader latches that invalidation in the queue fold when facts change.
func (s State) CompositionValid(e CompositionRecorded) bool {
 for _, m:=range e.Members {
  current,err:=s.CompositionMember(m.ChangeID,m.Revision,m.Head)
  if err!=nil || current.Alias!=m.Alias { return false }
  a,b:=current.Admission,m.Admission
  // Policy identity is semantic, while declarations/acknowledgements are acts.
  a.PolicyEventID,b.PolicyEventID=0,0
  a.ObservationIDs,b.ObservationIDs=nil,nil
  if !reflect.DeepEqual(a,b) || len(current.Observations)!=len(m.Observations) { return false }
  for i,o:=range current.Observations {
   old:=m.Observations[i]
   if o.Source!=old.Source || o.SourceRevision!=old.SourceRevision || o.Result!=old.Result { return false }
  }
 }
 return true
}

// RetainCompositions retires derived active evidence with the queue's batches.
// The immutable companions remain in the journal; a rebuilt batch has a new ID.
func (s State) RetainCompositions(batches []string) State {
 if s.compositions==nil { return s }
 s=s.clone()
 for id:=range s.compositions { if !slices.Contains(batches,id) { delete(s.compositions,id) } }
 return s
}
