DROP INDEX IF EXISTS idx_users_onboarding_email_token;
ALTER TABLE users DROP COLUMN IF EXISTS onboarding_email_opt_out_at;
ALTER TABLE users DROP COLUMN IF EXISTS onboarding_email_token;
DROP INDEX IF EXISTS idx_user_onboarding_email_due;
DROP TABLE IF EXISTS user_onboarding_email;
