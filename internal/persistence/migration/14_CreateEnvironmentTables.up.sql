CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE environment (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR NOT NULL,
    owner_id UUID REFERENCES users(id),
    organisation_id UUID REFERENCES organisation(id) DEFAULT NULL,
    secret_key BYTEA NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE environment_property (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    environment_id UUID REFERENCES environment(id) ON DELETE CASCADE,
    name VARCHAR NOT NULL,
    value BYTEA NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE environment_secret (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    environment_id UUID REFERENCES environment(id) ON DELETE CASCADE,
    name VARCHAR NOT NULL,
    value BYTEA NOT NULL,
    provider VARCHAR DEFAULT NULL,
    expires_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);