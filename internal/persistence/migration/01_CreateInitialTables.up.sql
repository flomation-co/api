CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE organisation (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR NOT NULL,
    icon VARCHAR,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE users (
   id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
   name VARCHAR NOT NULL,
   created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE organisation_user (
    organisation_id UUID REFERENCES organisation(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE flo (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name VARCHAR NOT NULL,
    organisation_id UUID REFERENCES organisation(id) ON DELETE NO ACTION,
    author_id UUID REFERENCES users(id) ON DELETE NO ACTION,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE revision (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    flo_id UUID NOT NULL REFERENCES flo(id) ON DELETE CASCADE,
    author_id UUID REFERENCES users(id) ON DELETE NO ACTION,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    data JSONB NOT NULL DEFAULT '{}'
);