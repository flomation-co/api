-- Company type for an organisation (Sole Trader, Limited Company, LLP, etc),
-- used on the Data Processing Agreement and to decide whether a company number
-- is required. Nullable: completed via the Organisation legal-details form.
ALTER TABLE organisation
    ADD COLUMN company_type TEXT;
