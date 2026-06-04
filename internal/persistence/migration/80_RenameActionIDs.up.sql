-- Rename action IDs across all flow revisions to match the new category structure.
-- This updates nodes[].type and nodes[].data.label in the revision JSONB data.

CREATE OR REPLACE FUNCTION rename_action_id(old_id TEXT, new_id TEXT) RETURNS void AS $$
DECLARE
    rev RECORD;
    updated_data JSONB;
    node JSONB;
    i INT;
    nodes_len INT;
BEGIN
    FOR rev IN SELECT id, data FROM revision
        WHERE data::text LIKE '%' || old_id || '%'
    LOOP
        updated_data := rev.data;
        nodes_len := jsonb_array_length(COALESCE(updated_data->'nodes', '[]'::jsonb));
        FOR i IN 0..nodes_len - 1 LOOP
            node := updated_data->'nodes'->i;
            IF node->>'type' = old_id THEN
                updated_data := jsonb_set(updated_data,
                    ARRAY['nodes', i::text, 'type'], to_jsonb(new_id));
            END IF;
            IF node->'data'->>'label' = old_id THEN
                updated_data := jsonb_set(updated_data,
                    ARRAY['nodes', i::text, 'data', 'label'], to_jsonb(new_id));
            END IF;
        END LOOP;
        IF updated_data IS DISTINCT FROM rev.data THEN
            UPDATE revision SET data = updated_data WHERE id = rev.id;
        END IF;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- Tools → Slack
SELECT rename_action_id('tools/slack_channels', 'slack/channels');
SELECT rename_action_id('tools/slack_users', 'slack/users');
SELECT rename_action_id('tools/slack_user_profile', 'slack/user_profile');
SELECT rename_action_id('tools/slack_history', 'slack/history');
SELECT rename_action_id('tools/slack_thread', 'slack/thread');
SELECT rename_action_id('tools/slack_react', 'slack/react');
SELECT rename_action_id('tools/slack_file_upload', 'slack/file_upload');
SELECT rename_action_id('tools/slack_search', 'slack/search');
SELECT rename_action_id('tools/slack_rich_message', 'slack/rich_message');
SELECT rename_action_id('tools/channel_action', 'slack/channel_action');

-- Messaging/Output → Slack
SELECT rename_action_id('messaging/slack', 'slack/send_message');
SELECT rename_action_id('output/slack_webhook', 'slack/webhook');

-- Tools → Google Gmail
SELECT rename_action_id('tools/email_draft', 'google/gmail/draft');
SELECT rename_action_id('tools/email_read', 'google/gmail/read');
SELECT rename_action_id('tools/email_send', 'google/gmail/send');
SELECT rename_action_id('tools/email_reply', 'google/gmail/reply');

-- Tools → Google Calendar
SELECT rename_action_id('tools/calendar_create', 'google/calendar/create');
SELECT rename_action_id('tools/calendar_read', 'google/calendar/read');
SELECT rename_action_id('tools/calendar_update', 'google/calendar/update');
SELECT rename_action_id('tools/calendar_delete', 'google/calendar/delete');

-- Tools → Google Accounts
SELECT rename_action_id('tools/google_accounts', 'google/accounts');

-- Tools → Web
SELECT rename_action_id('tools/web_fetch', 'web/fetch');
SELECT rename_action_id('tools/web_search', 'web/search');

-- HTTP → Web
SELECT rename_action_id('http/request', 'web/request');

-- Messaging → Platform consolidation
SELECT rename_action_id('messaging/twilio_sms', 'twilio/send_sms');
SELECT rename_action_id('messaging/telegram', 'messaging/telegram/send_message');
SELECT rename_action_id('messaging/telegram_voice', 'messaging/telegram/send_voice');
SELECT rename_action_id('messaging/email', 'messaging/email/send');

-- Output → Discord
SELECT rename_action_id('output/discord_webhook', 'messaging/discord/webhook');

-- Clean up function
DROP FUNCTION rename_action_id;

-- Delete old action records (action sync recreates them with new IDs on next startup)
DELETE FROM actions WHERE id LIKE 'tools/%';
DELETE FROM actions WHERE id IN (
    'messaging/twilio_sms',
    'messaging/slack',
    'messaging/telegram',
    'messaging/telegram_voice',
    'messaging/email',
    'output/slack_webhook',
    'output/discord_webhook',
    'http/request'
);
