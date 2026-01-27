CREATE TABLE queue (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    organisation_id UUID DEFAULT NULL REFERENCES organisation(id),
    name VARCHAR UNIQUE NOT NULL,
    registration_code VARCHAR NOT NULL DEFAULT substring(md5(random()::text), 0, 10),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    location_code CHAR(2) DEFAULT 'gb'
);

INSERT INTO queue (
    name,
    location_code
) VALUES (
    'Default Queue',
    'gb'
);

CREATE TABLE runner (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    identifier VARCHAR NOT NULL,
    name VARCHAR DEFAULT 'Flo Runner',
    registration_code VARCHAR NOT NULL,
    enrolled_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_contact_at TIMESTAMP DEFAULT NULL,
    ip VARCHAR
);

CREATE TABLE queue_runner (
    queue_id UUID REFERENCES queue(id) ON DELETE CASCADE,
    runner_id UUID REFERENCES runner(id) ON DELETE CASCADE
);

ALTER TABLE execution
    ADD COLUMN runner_id UUID DEFAULT NULL REFERENCES runner(id);

CREATE TABLE queue_organisation (
    queue_id UUID REFERENCES queue(id) ON DELETE CASCADE,
    organisation_id UUID REFERENCES organisation(id) ON DELETE CASCADE
)