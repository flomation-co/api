-- Extended user profile fields surfaced in flows as ${user.X} variables.
--
-- All columns are nullable TEXT so existing rows are untouched and users
-- can fill in only what they want to share. Empty strings vs NULL are
-- treated identically by the substitution layer (both collapse to "").
--
-- Personal:
--   salutation     Mr / Mrs / Ms / Mx / Dr / Prof / Other or free text
--   first_name
--   last_name
--   job_title      "Software Engineer", "Founder", etc.
--
-- Address (UK-format individual fields):
--   address_line_1
--   address_line_2
--   city
--   region         county/state
--   postcode
--   country
--
-- Composed variables computed at substitution time:
--   ${user.full_name}    = "<salutation> <first_name> <last_name>" collapsed
--   ${user.full_address} = neat multi-line block, empty lines elided

ALTER TABLE users
    ADD COLUMN salutation     TEXT,
    ADD COLUMN first_name     TEXT,
    ADD COLUMN last_name      TEXT,
    ADD COLUMN job_title      TEXT,
    ADD COLUMN address_line_1 TEXT,
    ADD COLUMN address_line_2 TEXT,
    ADD COLUMN city           TEXT,
    ADD COLUMN region         TEXT,
    ADD COLUMN postcode       TEXT,
    ADD COLUMN country        TEXT;
