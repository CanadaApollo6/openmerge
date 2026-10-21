# ADR 0008 PR 9c-2a evidence

Trial class: **Hold**. Baseline: local main
`c53f4253fb9c79ff8527494a7f0c307cbd6b1470` (#136). This implements the whole
9c-2a acquisition/config-blocking slice in one PR. Public authority activation
remains unavailable and production targets remain opted out. No deployment,
queueing, merging or organization acceptance is authorized.

Governing acquisition reads the landed immutable config and selected CODEOWNERS,
requires effective coverage of every supported policy location, and resolves
owners/membership/write grants through the forge. Proposed files, drafts,
verification plans and import acceptance cannot supply those identities or waive
missing evidence. Discovery and worker loading share that gate. New authority
bindings additionally require complete typed protection acquisition; same-binding
draftless rebinds retain the existing zero-seeing-read behavior. Adoption and
source observation keep independent reads after capturing their source basis.

Setup reads the server's default-branch ref and complete tree before planning
mutations. The actual landed queues remain in the protection-read inventory even
when the proposed file narrows selectors. Setup verification also acquires landed
evidence before recording verification. Import uses typed governing/protection
readers for landed authority policy, reports non-waivable blockers and retains
the public activation barrier. Ordinary GitHub import and workers retain their
existing parser/behavior.

Invalid landed authority config retains the last durable config/pin, blocks the
config source and stops further classification/admission until repair. Started
landing settlement remains before reconciliation. Transport failures expose only
sanitized classes while preserving error identity and quota retry information.

Verification receipts and the complete requirement/test map are recorded below
after the clean-tree offline driver completes. The driver is
`scripts/verify-adr0008-9c-2a.py`; it retains the existing strict comparator,
fixtures and frozen Makefile/CI flags.
