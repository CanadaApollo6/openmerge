-- Installation IDs belong to one App. Keep ownership outside queue identity so
-- independent Apps may share a database without revoking each other's queues.
CREATE TABLE installation_apps (
    installation text PRIMARY KEY,
    app_id text NOT NULL
);
