CREATE TABLE IF NOT EXISTS flo_favourite (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    flo_id UUID NOT NULL REFERENCES flo(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, flo_id)
);

CREATE INDEX IF NOT EXISTS idx_flo_favourite_user ON flo_favourite(user_id);
