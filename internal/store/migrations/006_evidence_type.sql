-- Historical landing explanations must not decode unrelated journal events.
-- Future compaction MUST retain landing_decision_recorded and the referenced
-- approval_eligibility_observed events, including their original read IDs.
CREATE INDEX events_landing_decision_identity ON events
    (queue_id, (payload->'data'->>'batch_id'), (payload->'data'->>'attempt_id'), id)
    WHERE type='landing_decision_recorded';
CREATE INDEX events_eligibility_read_identity ON events
    (queue_id, (payload->'data'->'basis'->>'change_id'),
     (payload->'data'->'basis'->>'read_id'), (payload->'data'->'basis'->>'revision'),
     (payload->'data'->'basis'->>'head'), id)
    WHERE type='approval_eligibility_observed';
