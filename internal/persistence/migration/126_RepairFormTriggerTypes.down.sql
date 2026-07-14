DELETE FROM trigger_type WHERE name IN (
    'typeform-webhook',
    'jotform-webhook',
    'surveymonkey-webhook'
);
