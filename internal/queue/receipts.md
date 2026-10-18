# Receipt producer contract for 11a

Use `State.RecordedBrief` for identity; its inputs come from recorded
classification, accountability and intent. Use `EvaluateApprovals` separately
for progress with fresh owner pools, then join it with
`brief.OverlayProgress(recorded, evaluation)` for display. A membership refresh cannot change the
recorded brief identity; a newly recorded classification can. Supply the live
semantic digest in `Policy.Digest`, computed by `changepolicy.PolicyDigest`
at the configuration boundary. `RecordedBrief` compares it to recorded
`Inputs.PolicyDigest` and returns typed `BriefPolicyMismatchError` on
mismatch (including an absent digest); 9/11a must defer or explicitly reject
that policy mismatch. It cannot recompute the semantic policy from the reduced
evaluator Policy DTO alone.

The command contains a bare digest. `State.PrepareBareReceipt` constructs
Supplied from Current's change, revision, head, endpoints, intent, semantic
policy and generation, with the supplied digest. It compares that digest to the
one recomputed from recorded evidence. There is no issued-identity ledger.
Explicit Supplied and Current bindings remain in the event for audit.

Digest parsing trims surrounding Unicode whitespace, then requires exactly 64
lowercase hexadecimal characters. Uppercase, internal whitespace, wrong lengths
and non-hex values fail parsing. 11a must pass the raw command digest to
`PrepareBareReceipt` even on parse failure: it prepares a rejected event with
`receipt_digest_malformed` for reviewer feedback, never silently ignores it.
The raw string is retained up to 1024 UTF-8 bytes (rune-aligned prefix);
invalid UTF-8 is replaced by U+FFFD before bounding. Valid input retains the
trimmed digest. Each Supplied string is valid UTF-8 and at most
1024 bytes, including rejected audit inputs. Missing evidence or another
`PrepareReceipt` error means **defer**: 11a must not manufacture a rejection.
Only a successfully prepared event may be journaled after admission preflight.

Stable rejection codes are `receipt_digest_malformed` (invalid syntax),
`receipt_digest_mismatch` (digest differs),
`receipt_binding_mismatch` (another explicit binding differs), and
`receipt_before_revision` (creation time is not strictly after the durable
revision boundary plus PR 6's 2-second clock allowance and 1-second timestamp
resolution). The event retains BoundaryAt; snapshots cross-check it and Basis
against that revision's head and durable boundary, including older revisions
retained specifically for receipt validation. Read time is never used as the revision boundary.

Receipt sources are pinned alongside blockers and barriers at source saturation.
An accepted record is never admitted without its matching stance source. Each
actor may retain at most 64 rejected receipts per change; a full actor budget
fails admission, preserving the prior state. Journal history remains the audit
record. Receipt, approval and classification bytes share the 48 MiB reserve; admission
also verifies the complete snapshot stays within the 64 MiB decoder limit.
Each admission check runs a full `EncodeSnapshot` (about 14 ms at 1,500
sources in the round-2 review measurement); 11a must include this cost in its
polling budget. This is a workload-specific measurement, not a latency bound.

V1 receipts are unsupported: nothing emitted v1 in production. They fail the
current v2 binding comparison (`brief.ValidateReceipt` returns
`receipt_binding_mismatch`; the producer normally uses `receipt_digest_mismatch`; another
explicit binding mismatch uses `receipt_binding_mismatch`). No v1 fallback
or issued-identity ledger exists.

7b owns the stance/provenance presentation section and structured TestLinks;
those placeholder fields are deliberately absent from the current Fact model.

Ordinary positive noise may be dropped at source capacity while its event marks
the epoch saturated and fail-closed. The stance preflight permits that durable
saturation transition; receipt admission additionally requires its exact pinned
approval source.
