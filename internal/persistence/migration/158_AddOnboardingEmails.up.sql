-- Onboarding email sequence.
--
-- Three "how to use the product" emails, enrolled at the moment an account is
-- first provisioned. One row per (user, email) so that:
--
--   * the primary key makes a double send impossible, however many API
--     instances are polling;
--   * every decision leaves evidence — sent, or skipped with the reason it
--     was skipped — which is the only way to answer "why did this person
--     get that email" after the fact;
--   * a sequence can be cancelled by marking its remaining rows skipped,
--     without deleting the record that they were ever scheduled.
--
-- Enrolment is deliberately explicit rather than derived from users.created_at.
-- Deriving it would enrol every existing account the moment this ships, so
-- several hundred people would receive a "create your first flow" email years
-- after creating their first flow.
CREATE TABLE IF NOT EXISTS user_onboarding_email (
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email_key   TEXT        NOT NULL,
    due_at      TIMESTAMPTZ NOT NULL,
    sent_at     TIMESTAMPTZ,
    skipped_at  TIMESTAMPTZ,
    skip_reason TEXT,
    attempts    INT         NOT NULL DEFAULT 0,
    last_error  TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, email_key)
);

-- The poller's only read: rows that are due and undecided. Partial index so it
-- stays small — a row leaves it for good once it is sent or skipped, which is
-- the steady state for all but a handful of rows at any moment.
CREATE INDEX IF NOT EXISTS idx_user_onboarding_email_due
    ON user_onboarding_email (due_at)
    WHERE sent_at IS NULL AND skipped_at IS NULL;

-- Opting out stops the sequence for the whole person, not one email, so it
-- belongs on the user rather than on a row of the schedule.
--
-- The token is what an unsubscribe link carries. It is separate from anything
-- else the account has because the link travels in plain text through mail
-- systems we do not control: the worst thing it can do is stop email arriving.
ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarding_email_token      UUID;
ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarding_email_opt_out_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_onboarding_email_token
    ON users (onboarding_email_token)
    WHERE onboarding_email_token IS NOT NULL;
