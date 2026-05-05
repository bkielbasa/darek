ALTER TABLE whatsapp_messages
    ADD COLUMN summarized_at timestamptz;

CREATE INDEX idx_whatsapp_messages_unsummarized
    ON whatsapp_messages (group_jid, sent_at)
    WHERE summarized_at IS NULL;
