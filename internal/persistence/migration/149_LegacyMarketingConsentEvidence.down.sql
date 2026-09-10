UPDATE users
SET marketing_consent_at     = NULL,
    marketing_consent_source = NULL
WHERE marketing_consent_source = 'legacy_opt_in_unknown_date';
