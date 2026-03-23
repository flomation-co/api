ALTER TABLE organisation ADD COLUMN IF NOT EXISTS allow_public_runners BOOLEAN NOT NULL DEFAULT true;
