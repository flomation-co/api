ALTER TABLE users
    DROP COLUMN IF EXISTS salutation,
    DROP COLUMN IF EXISTS first_name,
    DROP COLUMN IF EXISTS last_name,
    DROP COLUMN IF EXISTS job_title,
    DROP COLUMN IF EXISTS address_line_1,
    DROP COLUMN IF EXISTS address_line_2,
    DROP COLUMN IF EXISTS city,
    DROP COLUMN IF EXISTS region,
    DROP COLUMN IF EXISTS postcode,
    DROP COLUMN IF EXISTS country;
