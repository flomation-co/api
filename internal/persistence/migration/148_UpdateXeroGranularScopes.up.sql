-- Xero granular scopes migration.
--
-- Xero is replacing its broad OAuth scopes with granular ones. Apps created on
-- or after 2 March 2026 (which includes the Flomation-managed Xero app) can
-- ONLY request the new granular scopes — the broad `accounting.transactions`
-- and `accounting.reports.read` return `invalid_scope` at the authorize step.
--
-- Migration mapping (per Xero's granular-scopes guide):
--   accounting.transactions  -> accounting.invoices, accounting.banktransactions,
--                               accounting.payments, accounting.manualjournals
--   accounting.reports.read  -> individual report scopes (aged / balance sheet /
--                               profit and loss — the reports the Xero actions use)
--   accounting.contacts / accounting.settings / offline_access -> unchanged
--
-- This covers all shipped Xero actions: invoices, credit notes, quotes and
-- purchase orders (accounting.invoices); bank transactions and transfers
-- (accounting.banktransactions); payments; manual journals; accounts, items,
-- tax rates, tracking categories and organisation (accounting.settings);
-- contacts; and the aged/balance-sheet/P&L report actions.
UPDATE credential_provider
SET default_scopes = 'openid profile email accounting.contacts accounting.settings accounting.invoices accounting.banktransactions accounting.payments accounting.manualjournals accounting.reports.aged.read accounting.reports.balancesheet.read accounting.reports.profitandloss.read offline_access'
WHERE slug = 'xero';
