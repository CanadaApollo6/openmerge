# Receipt producer contract for 11a

Use `State.RecordedBrief` for identity; its inputs come from recorded
classification, accountability and intent. Use `EvaluateApprovals` separately
for progress with fresh owner pools. A membership refresh cannot change the
recorded brief identity; a newly recorded classification can.

The command contains a bare digest. `State.PrepareBareReceipt` constructs
Supplied from Current's change, revision, head, endpoints, intent, semantic
policy and generation, with the supplied digest. It compares that digest to the
one recomputed from recorded evidence. There is no issued-identity ledger.
Explicit Supplied and Current bindings remain in the event for audit.

Digest parsing trims surrounding Unicode whitespace, then requires exactly 64
lowercase hexadecimal characters. Uppercase, internal whitespace, wrong lengths
and non-hex values fail parsing. Each Supplied string is valid UTF-8 and at most
1024 bytes, including rejected audit inputs. Missing evidence or another
`PrepareReceipt` error means **defer**: 11a must not manufacture a rejection.
Only a successfully prepared event may be journaled after admission preflight.

Stable rejection codes are `receipt_digest_mismatch` (digest differs),
`receipt_binding_mismatch` (another explicit binding differs), and
`receipt_before_revision` (creation time is not strictly after the durable
revision boundary plus PR 6's 2-second clock allowance and 1-second timestamp
resolution). The event retains BoundaryAt; snapshots cross-check it and Basis
against the retained revision. Read time is never used as the revision boundary.

Receipt sources are pinned alongside blockers and barriers at source saturation.
An accepted record is never admitted without its matching stance source. Each
actor may retain at most 64 rejected receipts per change; a full actor budget
fails admission, preserving the prior state. Journal history remains the audit
record. Receipt, approval and classification bytes share the 48 MiB reserve; admission
also verifies the complete snapshot stays within the 64 MiB decoder limit.

7b owns the stance/provenance presentation section and structured TestLinks;
those placeholder fields are deliberately absent from the current Fact model.

Ordinary positive noise may be dropped at source capacity while its event marks
the epoch saturated and fail-closed. The stance preflight permits that durable
saturation transition; receipt admission additionally requires its exact pinned
approval source.
