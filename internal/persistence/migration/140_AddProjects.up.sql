-- Projects: a nested (arbitrary-depth) grouping of flows. A flow belongs to at
-- most one project (project_id NULL = ungrouped, org-wide as before). parent_id
-- NULL = top-level project. Personal-mode projects have organisation_id NULL,
-- mirroring the environment table.
CREATE TABLE IF NOT EXISTS project (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR NOT NULL,
    description TEXT,
    parent_id UUID REFERENCES project(id) ON DELETE SET NULL,
    organisation_id UUID REFERENCES organisation(id) DEFAULT NULL,
    owner_id UUID REFERENCES users(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    archived_at TIMESTAMPTZ DEFAULT NULL
);

CREATE INDEX IF NOT EXISTS idx_project_parent ON project(parent_id);
CREATE INDEX IF NOT EXISTS idx_project_org ON project(organisation_id);
CREATE INDEX IF NOT EXISTS idx_project_owner ON project(owner_id);

ALTER TABLE flo
    ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES project(id) ON DELETE SET NULL DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_flo_project ON flo(project_id);
