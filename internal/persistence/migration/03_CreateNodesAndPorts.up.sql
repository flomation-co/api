CREATE TABLE port (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type VARCHAR NOT NULL,
    name VARCHAR NOT NULL,
    label VARCHAR NOT NULL,
    colour VARCHAR NOT NULL DEFAULT 'grey',
    controls JSONB DEFAULT '[]'
);

CREATE TABLE node (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type VARCHAR NOT NULL UNIQUE,
    category VARCHAR NOT NULL,
    label VARCHAR NOT NULL,
    description VARCHAR NOT NULL,
    inputs JSONB DEFAULT '[]',
    outputs JSONB DEFAULT '[]',
    module VARCHAR NOT NULL DEFAULT 'common'
);

INSERT INTO port (type, name, label, colour) VALUES ('boolean', 'boolean', 'Boolean', 'blue');
INSERT INTO port (type, name, label, colour) VALUES ('string', 'string', 'Text', 'green');
INSERT INTO port (type, name, label, colour) VALUES ('integer', 'integer', 'Integer', 'pink');

INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('concatenate', 'Common', 'Concatenate', 'Concatenates two strings', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('addNumber', 'Arithmetic', 'Add Numbers', 'Adds two integers together', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('int2string', 'Convert', 'Integer to String', 'Converts an Integer to a String', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('print', 'Debug', 'Print', 'Output a message to the console', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('string', 'Constant', 'String', 'Constant representation of a String', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('integer', 'Constant', 'Integer', 'Constant representation of a Integer', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('email', 'Notify', 'Email', 'Send an email', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('output-string', 'Output', 'String', 'String output from Flo', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('output-integer', 'Output', 'Integer', 'Integer output from Flo', '[]', '[]');
INSERT INTO node (type, category, label, description, inputs, outputs) VALUES ('output-boolean', 'Output', 'Boolean', 'Boolean output from Flo', '[]', '[]');
