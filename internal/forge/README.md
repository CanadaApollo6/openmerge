# Forge observations

`Driver` retains the legacy `Approval` and command-discovery `Comment` views.
`IdentityReader` and `NativeReviewReader` are separate optional capabilities;
Stage A 3b adds no production caller. Native acquisition never reads permissions,
folds stances, or authorizes a user. Account kinds describe forge accounts, not
configured accountability or agent mappings.

## Native review input contract for PR 6

`NativeReviews` succeeds only after all pages of reviews, issue comments, inline
review comments and any required dismissal timeline have been read. An error
invalidates the entire observation. `ErrNativeReviewsUnavailable` means unknown,
including inaccessible/403/404 responses, invalid source identities or malformed
responses. `ErrNativeReviewsIncomplete` identifies a pagination bound, unsupported
pagination or ambiguous dismissal events; later-page HTTP failures wrap both
sentinels. Never reuse earlier success as fresh evidence after a failure.
Known-empty lists succeed. These reads are separate observations, not an atomic
snapshot of concurrent remote edits. The GitHub reader bounds each collection
at 100 pages of 100 records and rejects cyclic, skipped or malformed next links.

Reviews retain all native states, duplicates and source order. Consumers must
reconcile by stable source IDs, never by slice order, login or observation order.
`comment` and `pending` reviews are neither approvals nor withdrawals. Issue and
inline comments have distinct namespaces (`Kind`); only issue-thread comments
are the existing command channel. No edit time replaces a comment's creation
time. Native approvals bind `CommitID`, with no inferred current-head or intent
binding. Review/comment authors carry `UserIdentity` from that source response;
subsequent authorization must use `IdentityReader.WriteGrant` with that stable ID,
even if the login was renamed/reassigned between reads.

PR 6 converts these DTOs to `changepolicy.StanceSource` (forge does not import
changepolicy):

- Normal review: channel `NativeReview`; decimal review `ID` as source `ID` and
  native ID as `Sequence`; `Submitted.Time` as `Time`. Author ID is the stance
  user and acting user. Map `approve` / `request_changes` to their stance kinds.
- Dismissal: stance `Withdraw` attributed to the **review author**, retaining
  `CommitID` for revision attribution. Use decimal `Dismissal.EventID` as source
  `ID`/`Sequence` when known, otherwise the review ID with explicit fallback
  provenance. Always set `DismissedReviewID` to the dismissed review ID and
  `ActingUserID` to the dismissal actor's ID when known (empty when unknown).
  Event/review IDs are independent namespaces; they are not interchangeable
  ordering counters. Keep source kind with the durable evidence.
- An authenticated issue-comment command uses channel `CommentCommand`, comment
  `ID`/`Sequence`, `Created.Time` and the comment author's stable ID. Acquisition
  itself performs no command parsing or revision attribution.
- Authoritative source timestamps set `TimeKnown=true`, `Precision=ExactTime`;
  retain their one-second resolution and `UpperBound` in durable evidence.
  A fallback sets `TimeKnown=false`, `Precision=ObservationFallback` and
  `UpperTimeBound=Dismissal.Time.UpperBound`. Its `Time` records the forge's
  response Date, **not** the action time. Preserve `NativeTime.Source`, duration
  precision/uncertainty and `NativeObservation` in PR 6's observation record;
  the shared `StanceSource` alone does not retain all acquisition provenance.

The GitHub reviews API normally omits dismissal time, so the reader joins
`review_dismissed` timeline events by `dismissed_review.review_id`. Event ID,
actor and creation time are distinct from the original review's ID, author and
submission. A provider's explicit `dismissed_at`/`dismissed_by` extension is also
accepted; timeline still supplies event identity. A readable complete timeline
without an authoritative dismissal timestamp yields an explicit fallback.
An unreadable timeline fails closed, even if the review supplied a time.

The fallback bound is response Date + one second timestamp precision + two
seconds clock allowance. Missing/invalid Date leaves the bound zero; local
`ObservedAt` is retained but cannot substitute for the forge clock. Each
conditional read revalidates through the installation transport and records the
response Date, including on 304. The allowance is an explicit acquisition
assumption, not a guarantee of arbitrary server clock accuracy.

Q4 requires PR 6 to persist the bound at the **first** observation of a distinct
dismissal, keyed by the stable dismissed review ID and associated event ID when
available. Later discovery of an event ID must join that same dismissal, not
create a new blocker. Polling and restart must not renew the barrier; only a
distinct newer blocking action may raise it. Acquisition deliberately has no
barrier cache: its later observations expose later Date values so the durable
consumer can retain the first bound. Reobserve before accepting a clearing
review/receipt. Its source time must be strictly after the pinned conservative
upper bound, with no newer blocking stance; ties or ambiguous times block.
Never use the dismissed review's original submission time for this comparison.
Without a comparable bound, require a new revision and a fresh review/receipt
attributed to it, keeping the old withdrawal historical. PR 6 owns persistence,
restart and same-revision recovery tests.

All new GitHub reads use the existing installation transport, conditional cache
and budget. Settlement contexts are demoted to active for these reads; other
request priorities are retained. They cannot consume settlement's reserved
capacity. There are no new public keys, events, actions or production call sites.
