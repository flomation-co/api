CREATE TABLE IF NOT EXISTS dashboard (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(255) NOT NULL,
    description TEXT,
    owner_id VARCHAR(255) NOT NULL,
    organisation_id UUID,
    is_public BOOLEAN NOT NULL DEFAULT FALSE,
    public_slug VARCHAR(64) UNIQUE,
    refresh_interval INTEGER NOT NULL DEFAULT 0,
    time_range VARCHAR(20) NOT NULL DEFAULT '24h',
    time_range_from TIMESTAMPTZ,
    time_range_to TIMESTAMPTZ,
    layout_columns INTEGER NOT NULL DEFAULT 12,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS dashboard_widget (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dashboard_id UUID NOT NULL REFERENCES dashboard(id) ON DELETE CASCADE,
    title VARCHAR(255) NOT NULL,
    widget_type VARCHAR(30) NOT NULL,
    flo_id UUID,
    config JSONB NOT NULL DEFAULT '{}',
    grid_x INTEGER NOT NULL DEFAULT 0,
    grid_y INTEGER NOT NULL DEFAULT 0,
    grid_w INTEGER NOT NULL DEFAULT 3,
    grid_h INTEGER NOT NULL DEFAULT 2,
    ordering INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS dashboard_widget_data (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    widget_id UUID NOT NULL UNIQUE REFERENCES dashboard_widget(id) ON DELETE CASCADE,
    execution_id UUID,
    data JSONB,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status VARCHAR(20) NOT NULL DEFAULT 'pending'
);

CREATE INDEX IF NOT EXISTS idx_dashboard_owner_id ON dashboard(owner_id);
CREATE INDEX IF NOT EXISTS idx_dashboard_organisation_id ON dashboard(organisation_id);
CREATE INDEX IF NOT EXISTS idx_dashboard_public_slug ON dashboard(public_slug);
CREATE INDEX IF NOT EXISTS idx_dashboard_widget_dashboard_id ON dashboard_widget(dashboard_id);
CREATE INDEX IF NOT EXISTS idx_dashboard_widget_data_widget_id ON dashboard_widget_data(widget_id);
