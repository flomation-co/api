INSERT INTO organisation (
    id,
    name
) VALUES (
    '0497afa5-67ce-446d-83c3-431094d041b7',
    'Get With The Flo Limited'
);

INSERT INTO users (
    id,
    name
) VALUES (
    'e4c9a02d-e99c-4a16-b7ea-953eafd699b6',
    'Sandy B'
);

INSERT INTO organisation_user (organisation_id, user_id) VALUES ('0497afa5-67ce-446d-83c3-431094d041b7', 'e4c9a02d-e99c-4a16-b7ea-953eafd699b6')