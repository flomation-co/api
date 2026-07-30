-- Per-project access control: a project can be shared with one or more Teams
-- (organisation_group) at a role. A project with no grants (on itself or any
-- ancestor) is "open" — org-wide visible as in Phase 1. A project with any
-- effective grant is "restricted" to the granted teams. Grants inherit down the
-- tree (enforced in application logic).
CREATE TABLE IF NOT EXISTS project_group (
    project_id UUID NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    group_id   UUID NOT NULL REFERENCES organisation_group(id) ON DELETE CASCADE,
    role       VARCHAR NOT NULL DEFAULT 'view',  -- view | edit | manage
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_project_group_group ON project_group(group_id);
