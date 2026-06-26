-- +goose Up
-- Addresses that must not be emailed again because a prior send hard-bounced
-- (unknown mailbox, blocklisted, etc.). Sending to these damages our sender
-- reputation and raises the hard-bounce rate, so dispatch() skips them.
CREATE TABLE email_suppressions (
    email      TEXT NOT NULL PRIMARY KEY,
    reason     TEXT NOT NULL,
    source     TEXT NOT NULL DEFAULT 'scaleway_poll',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE email_suppressions;
