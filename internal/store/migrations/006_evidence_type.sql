-- Historical landing explanations must not decode unrelated journal events.
-- Future compaction MUST retain landing_decision_recorded and the referenced
-- approval_eligibility_observed events, including their original read IDs.
CREATE INDEX events_queue_type ON events(queue_id, type, id);
