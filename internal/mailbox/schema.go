package mailbox

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaSQL = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS endpoints (
  endpoint_id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS endpoint_addresses (
  address TEXT PRIMARY KEY,
  endpoint_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY (endpoint_id) REFERENCES endpoints(endpoint_id)
);

CREATE TABLE IF NOT EXISTS persons (
  person_id TEXT PRIMARY KEY,
  person TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS groups (
  group_id TEXT PRIMARY KEY,
  address TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  metadata_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS group_memberships (
  membership_id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  person_id TEXT NOT NULL,
  joined_at TEXT NOT NULL,
  left_at TEXT,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  FOREIGN KEY (group_id) REFERENCES groups(group_id),
  FOREIGN KEY (person_id) REFERENCES persons(person_id),
  CHECK (left_at IS NULL OR left_at >= joined_at)
);

CREATE TABLE IF NOT EXISTS messages (
  message_id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  sender_endpoint_id TEXT,
  subject TEXT NOT NULL,
  content_type TEXT NOT NULL,
  schema_version TEXT NOT NULL,
  idempotency_key TEXT,
  body_blob_ref TEXT NOT NULL,
  body_size INTEGER NOT NULL,
  body_sha256 TEXT NOT NULL,
  forwarded_message_id TEXT,
  forwarded_from_address TEXT,
  reply_to_message_id TEXT,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  FOREIGN KEY (sender_endpoint_id) REFERENCES endpoints(endpoint_id),
  FOREIGN KEY (forwarded_message_id) REFERENCES messages(message_id),
  FOREIGN KEY (reply_to_message_id) REFERENCES messages(message_id)
);

CREATE TABLE IF NOT EXISTS deliveries (
  delivery_id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL,
  recipient_endpoint_id TEXT NOT NULL,
  state TEXT NOT NULL,
  visible_at TEXT NOT NULL,
  lease_token TEXT,
  lease_expires_at TEXT,
  acked_at TEXT,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  last_error_code TEXT,
  last_error_text TEXT,
  FOREIGN KEY (message_id) REFERENCES messages(message_id),
  FOREIGN KEY (recipient_endpoint_id) REFERENCES endpoints(endpoint_id)
);

CREATE TABLE IF NOT EXISTS group_messages (
  message_id TEXT PRIMARY KEY,
  group_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  eligible_count INTEGER NOT NULL,
  FOREIGN KEY (message_id) REFERENCES messages(message_id),
  FOREIGN KEY (group_id) REFERENCES groups(group_id)
);

CREATE TABLE IF NOT EXISTS group_message_eligibility (
  message_id TEXT NOT NULL,
  person_id TEXT NOT NULL,
  membership_id TEXT NOT NULL,
  eligible_at TEXT NOT NULL,
  PRIMARY KEY (message_id, person_id),
  FOREIGN KEY (message_id) REFERENCES group_messages(message_id),
  FOREIGN KEY (person_id) REFERENCES persons(person_id),
  FOREIGN KEY (membership_id) REFERENCES group_memberships(membership_id)
);

CREATE TABLE IF NOT EXISTS group_reads (
  message_id TEXT NOT NULL,
  person_id TEXT NOT NULL,
  first_read_at TEXT NOT NULL,
  PRIMARY KEY (message_id, person_id),
  FOREIGN KEY (message_id) REFERENCES group_messages(message_id),
  FOREIGN KEY (person_id) REFERENCES persons(person_id)
);

CREATE TABLE IF NOT EXISTS events (
  event_id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  event_type TEXT NOT NULL,
  endpoint_id TEXT,
  message_id TEXT,
  delivery_id TEXT,
  detail_json TEXT NOT NULL DEFAULT '{}',
  FOREIGN KEY (endpoint_id) REFERENCES endpoints(endpoint_id),
  FOREIGN KEY (message_id) REFERENCES messages(message_id),
  FOREIGN KEY (delivery_id) REFERENCES deliveries(delivery_id)
);

CREATE INDEX IF NOT EXISTS idx_endpoint_addresses_endpoint_id
  ON endpoint_addresses (endpoint_id);

CREATE INDEX IF NOT EXISTS idx_group_memberships_group_joined
  ON group_memberships (group_id, joined_at, membership_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_group_memberships_active
  ON group_memberships (group_id, person_id)
  WHERE left_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_group_memberships_person_joined
  ON group_memberships (person_id, joined_at, membership_id);

CREATE INDEX IF NOT EXISTS idx_deliveries_recipient_state_visible
  ON deliveries (recipient_endpoint_id, state, visible_at);

CREATE INDEX IF NOT EXISTS idx_deliveries_message_id
  ON deliveries (message_id);

CREATE INDEX IF NOT EXISTS idx_group_messages_group_created
  ON group_messages (group_id, created_at, message_id);

CREATE INDEX IF NOT EXISTS idx_group_message_eligibility_person_message
  ON group_message_eligibility (person_id, message_id);

CREATE INDEX IF NOT EXISTS idx_group_reads_person_message
  ON group_reads (person_id, message_id);

CREATE INDEX IF NOT EXISTS idx_events_message_delivery
  ON events (message_id, delivery_id);
`

func initSchema(ctx context.Context, db *sql.DB) error {
	if err := migrateLegacyGroupMessageSchema(ctx, db); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	if err := migrateForwardedMessageSchema(ctx, db); err != nil {
		return err
	}
	return nil
}
