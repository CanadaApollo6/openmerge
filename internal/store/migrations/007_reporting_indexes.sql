-- Read projections must not walk unrelated journal events to resolve legacy
-- PR aliases, page immutable decisions, or locate an attempt's settlement.
-- These are reporting indexes only; event and action wire formats are unchanged.
CREATE INDEX events_reporting_pr_alias ON events
    (queue_id, (CASE
        WHEN payload->'data'->>'pr' ~ '^[+-]?[0-9]{1,19}$' THEN
            CASE
                WHEN (payload->'data'->>'pr')::numeric BETWEEN -9223372036854775808 AND 9223372036854775807
                THEN (payload->'data'->>'pr')::bigint
            END
    END))
    WHERE type IN ('pr_observed','change_revision_observed');
CREATE INDEX events_reporting_change_decision ON events
    (queue_id, (COALESCE(payload->'data'->'approval'->'basis'->>'change_id', payload->'data'->'remedy'->>'change_id')), id)
    WHERE type='landing_decision_recorded';
CREATE INDEX events_reporting_settlement ON events
    (queue_id, (payload->'data'->>'batch_id'), id)
    WHERE type IN ('landed','landing_abandoned','landing_started');
