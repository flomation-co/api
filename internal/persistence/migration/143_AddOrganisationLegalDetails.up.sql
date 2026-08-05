-- Legal-entity details for an organisation, used to identify the Controller on
-- the generated Data Processing Agreement (and reusable on invoices etc). All
-- nullable: an organisation may not have completed them yet, in which case the
-- DPA falls back to the organisation display name and the requesting admin's
-- contact details.
ALTER TABLE organisation
    ADD COLUMN legal_name     TEXT,
    ADD COLUMN company_number TEXT,
    ADD COLUMN address_line_1 TEXT,
    ADD COLUMN address_line_2 TEXT,
    ADD COLUMN city           TEXT,
    ADD COLUMN region         TEXT,
    ADD COLUMN postcode       TEXT,
    ADD COLUMN country        TEXT;
