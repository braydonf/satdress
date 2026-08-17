CREATE TABLE IF NOT EXISTS "request_events" (`id` integer,`nostr_id` text UNIQUE,`user` text,`pub_key` text,`raw` text,`status` text,`created_at` datetime,`updated_at` datetime, `expires_at` datetime,PRIMARY KEY (`id`));
CREATE UNIQUE INDEX IF NOT EXISTS `idx_request_events_nostr_id` ON `request_events`(`nostr_id`);
CREATE TABLE IF NOT EXISTS "response_events" (`id` integer,`nostr_id` text UNIQUE,`request_nostr_id` text,`user` text,`pub_key` text,`raw` text,`status` text,`created_at` datetime,`updated_at` datetime,PRIMARY KEY (`id`));
CREATE UNIQUE INDEX IF NOT EXISTS `idx_response_events_nostr_id` ON `response_events`(`nostr_id`);
CREATE UNIQUE INDEX IF NOT EXISTS `idx_response_events_request_nostr_id` ON `response_events`(`request_nostr_id`);
CREATE INDEX IF NOT EXISTS `idx_request_events_user_status` ON `request_events`(`user`, `status`);
