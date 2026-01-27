CREATE TYPE action_type AS ENUM('0', '1', '2', '3', '4', '5');

CREATE TABLE actions (
    id VARCHAR PRIMARY KEY,
    name VARCHAR UNIQUE NOT NULL,
    action_type action_type NOT NULL DEFAULT '0',
    description VARCHAR NOT NULL,
    icon VARCHAR NOT NULL,
    plugin VARCHAR,
    ordering BIGINT,
    inputs JSONB,
    outputs JSONB
);

-- Triggers = 1
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('manual', 'Manual', '1', 'Manually invoke a flow', '["fa", "play"]', 'trigger/manual', 0, null, '[{"name": "time", "value": "Start Time", "type": "timestamp"}]');
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('form', 'Customisable Form', '1', 'A dynamic, customisable form for users to fill in', '["fa", "text"]','trigger/form', 1, null, '[{"name": "time", "value": "Start Time", "type": "timestamp"}, {"name": "form_data", "value": "Form Data", "type": "form"}]');

-- Processing = 2
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('stringConcatenation', 'String Concatenation', '2', 'Join two strings together', '["fa", "quote-left"]','string/concatenate', 0, '[{"name": "input_1", "value": "String 1", "type": "string"}, {"name": "input_2", "value": "String 2", "type": "string"}]', '[{"name": "output", "value": "Output", "type": "string"}]');
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('stringSplit', 'String Split', '2', 'Split a string into parts at a delimiter', '["fa", "scissors"]','string/split', 1, '[{"name": "input", "value": "Input", "type": "string"}, {"name": "delimiter", "value": "Delimiter", "type": "string"}]', '[{"name": "output", "value": "Output", "type": "list(string)"}]');
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('function', 'Function', '2', 'Run a generic JavaScript function', '["fa", "function"]','common/function', 2, '[{"name": "inputs", "value": "Inputs", "type": "object"}]', '[{"name": "outputs", "value": "Outputs", "type": "object"}]');
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('subflow', 'Trigger Flow', '2', 'Trigger another Flow, passing in data to its inputs', '["fa", "flag-checkered"]','common/subflow', 3, '[{"name": "inputs", "value": "Inputs", "type": "object"}, {"name": "wait_on_completion", "value": "Wait for Completion", "type": "boolean"}]', '[{"name": "outputs", "value": "Outputs", "type": "object"}]');

-- Outputs = 3
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('webook', 'Webhook', '3', 'Trigger a remote webhook', '["fa", "webhook"]','common/webhook', 0, '[{"name": "url", "value": "URL", "type": "string"}, {"name": "message", "value": "Message", "type": "string"}]', null);
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('slackMessage', 'Slack Message', '3', 'Send a message to a Slack channel', '["fab", "slack"]','slack/message', 1, '[{"name": "url", "value": "URL", "type": "string"}, {"name": "message", "value": "Message", "type": "string"}]', null);

-- Conditional = 4
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('if', 'If', '4', 'Only continue if a condition is satisfied', '["fa", "code-branch"]','condition/if', 0, '[{"name": "input", "value": "Input", "type": "object"}, {"name": "condition", "value": "Condition", "type": "enum(\"Equals\", \"Not Equals\", \"Greater Than\", \"Greater Than or Equal\", \"Less Than\", \"Less Than or Equal\")"}]', null);

-- Loop = 5
INSERT INTO actions (id, name, action_type, description, icon, plugin, ordering, inputs, outputs) VALUES ('loop', 'Loop', '5', 'Loop a specified number of times', '["fa", "recycle"]','loop/loop', 0, '[{"name": "input", "value": "Input", "type": "object"}, {"name": "count", "value": "Count", "type": "integer"}]', null);