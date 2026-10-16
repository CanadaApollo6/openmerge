ALTER TABLE actions
    ADD COLUMN poison_count integer NOT NULL DEFAULT 0 CHECK (poison_count >= 0),
    ADD COLUMN poison_notified boolean NOT NULL DEFAULT false;

UPDATE actions SET poison_count = 1 WHERE status = 'poisoned';
