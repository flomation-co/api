CREATE TYPE ExecutionState AS ENUM (
    'created',
    'allocated',
    'running',
    'executed'
);

CREATE TYPE CompletionState AS ENUM (
    'pending',
    'success',
    'fail',
    'cancel',
    'timeout'
);

CREATE TABLE trigger_type (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR NOT NULL
);

INSERT INTO trigger_type (name) VALUES ('manual');

CREATE TABLE trigger (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR NOT NULL,
    owner_id UUID REFERENCES users(id),
    organisation_id UUID DEFAULT NULL REFERENCES organisation(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    type UUID NOT NULL REFERENCES trigger_type(id)
);

CREATE TABLE trigger_invocation (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    trigger_id UUID REFERENCES trigger(id),
    owner_id UUID REFERENCES users(id),
    organisation_id UUID DEFAULT NULL REFERENCES organisation(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    data JSONB DEFAULT NULL
);

CREATE TABLE execution (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flo_id UUID NOT NULL REFERENCES flo(id) ON DELETE NO ACTION,
    name VARCHAR NOT NULL,
    owner_id UUID REFERENCES users(id),
    organisation_id UUID DEFAULT NULL REFERENCES organisation(id),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT NULL,
    completed_at TIMESTAMP DEFAULT NULL,
    triggered_by UUID REFERENCES trigger_invocation(id),
    execution_status ExecutionState DEFAULT 'created',
    completion_status CompletionState DEFAULT 'pending'
);