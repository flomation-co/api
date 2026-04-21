CREATE TABLE IF NOT EXISTS eula (
    id          SERIAL PRIMARY KEY,
    version     INTEGER NOT NULL UNIQUE,
    content     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO eula (version, content) VALUES (1, 'END USER LICENCE AGREEMENT

Effective Date: 21 April 2026

This End User Licence Agreement ("Agreement") is a legal agreement between you ("User") and Flomation Ltd ("Company"), governing your use of the Flomation platform, including all associated services, applications, and tools (collectively, the "Platform").

By accessing or using the Platform, you acknowledge that you have read, understood, and agree to be bound by the terms of this Agreement. If you do not agree to these terms, you must not use the Platform.

1. LICENCE GRANT

Subject to your compliance with this Agreement, the Company grants you a limited, non-exclusive, non-transferable, revocable licence to access and use the Platform for your internal business or personal purposes, in accordance with the subscription plan you have selected.

2. ACCOUNT REGISTRATION

You must register an account to use the Platform. You agree to provide accurate, current, and complete information during registration and to keep your account details up to date. You are responsible for maintaining the confidentiality of your login credentials and for all activities that occur under your account.

3. ACCEPTABLE USE

You agree not to:
  a) Use the Platform for any unlawful, fraudulent, or harmful purpose;
  b) Attempt to gain unauthorised access to any part of the Platform or its infrastructure;
  c) Reverse engineer, decompile, or disassemble any part of the Platform;
  d) Interfere with or disrupt the integrity or performance of the Platform;
  e) Use the Platform to transmit viruses, malware, or other harmful code;
  f) Resell, sublicence, or redistribute access to the Platform without prior written consent.

4. INTELLECTUAL PROPERTY

All rights, title, and interest in and to the Platform, including all intellectual property rights, are and shall remain the exclusive property of the Company. This Agreement does not grant you any rights to the Company''s trademarks, logos, or brand features.

5. DATA AND PRIVACY

Your use of the Platform is subject to our Privacy Policy, available at https://www.flomation.co/privacy. By using the Platform, you consent to the collection, use, and processing of your data as described therein.

You retain ownership of any data you upload to or create within the Platform ("User Data"). You grant the Company a limited licence to process User Data solely for the purpose of providing and improving the Platform.

6. AVAILABILITY AND SUPPORT

The Company shall use reasonable endeavours to maintain the availability of the Platform but does not guarantee uninterrupted or error-free service. Scheduled maintenance windows will be communicated in advance where practicable.

7. LIMITATION OF LIABILITY

To the maximum extent permitted by law:
  a) The Platform is provided "as is" and "as available" without warranties of any kind, whether express or implied;
  b) The Company shall not be liable for any indirect, incidental, special, consequential, or punitive damages;
  c) The Company''s total aggregate liability shall not exceed the fees paid by you in the twelve (12) months preceding the claim.

8. INDEMNIFICATION

You agree to indemnify and hold harmless the Company and its officers, directors, employees, and agents from any claims, losses, damages, liabilities, and expenses (including legal fees) arising from your use of the Platform or breach of this Agreement.

9. TERMINATION

Either party may terminate this Agreement at any time. The Company may suspend or terminate your access to the Platform immediately if you breach any provision of this Agreement. Upon termination, your right to use the Platform ceases immediately. Provisions that by their nature should survive termination shall continue in effect.

10. MODIFICATIONS

The Company reserves the right to modify this Agreement at any time. Material changes will be communicated via the Platform or email. Your continued use of the Platform following such notification constitutes acceptance of the revised terms.

11. GOVERNING LAW

This Agreement shall be governed by and construed in accordance with the laws of England and Wales. Any disputes arising under this Agreement shall be subject to the exclusive jurisdiction of the courts of England and Wales.

12. GENERAL

  a) This Agreement constitutes the entire agreement between you and the Company regarding the Platform.
  b) If any provision of this Agreement is found to be unenforceable, the remaining provisions shall continue in full force and effect.
  c) The Company''s failure to enforce any right or provision shall not constitute a waiver of such right or provision.
  d) You may not assign or transfer this Agreement without the Company''s prior written consent.

Contact Information:
Flomation Ltd
hello@flomation.co
https://www.flomation.co');

ALTER TABLE users ADD COLUMN IF NOT EXISTS eula_version INTEGER DEFAULT 0;
ALTER TABLE users ADD COLUMN IF NOT EXISTS eula_accepted_at TIMESTAMPTZ;
