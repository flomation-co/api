-- Seed the oci_key credential provider — a managed OCI API-signing-key connector.
-- Like aws_role (132) this is a token-less credential: there is NO OAuth round
-- trip, so auth_url/token_url (NOT NULL on the table) are stored empty and never
-- used. Flomation generates the RSA keypair; the private key PEM is encrypted into
-- environment_credential.access_token, and the non-secret fields
-- (tenancy_ocid, user_ocid, region, fingerprint, compartment_ocid, scope,
-- public_key, stack_token) live in the plaintext metadata JSONB. The executor
-- signs OCI requests with the key — the same universal auth every OCI service
-- accepts — so coverage is never conditional on which service a flow calls.
--
-- NUMBERING: 137. origin/main tops out at 136 when this is written. golang-migrate
-- silently skips an out-of-order version and a duplicate makes the api fail to
-- boot, so re-check this number after rebasing.
INSERT INTO credential_provider (slug, name, icon, auth_url, token_url, revoke_url, default_scopes)
VALUES ('oci_key', 'Oracle Cloud (API key)', 'oracle', '', '', NULL, NULL)
ON CONFLICT (slug) DO NOTHING;
