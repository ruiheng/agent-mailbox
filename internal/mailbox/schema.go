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

CREATE TABLE IF NOT EXISTS endpoint_aliases (
  alias TEXT PRIMARY KEY,
  endpoint_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY (endpoint_id) REFERENCES endpoints(endpoint_id)
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
  reply_to_message_id TEXT,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  FOREIGN KEY (sender_endpoint_id) REFERENCES endpoints(endpoint_id),
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

CREATE INDEX IF NOT EXISTS idx_endpoint_aliases_endpoint_id
  ON endpoint_aliases (endpoint_id);

CREATE INDEX IF NOT EXISTS idx_deliveries_recipient_state_visible
  ON deliveries (recipient_endpoint_id, state, visible_at);

CREATE INDEX IF NOT EXISTS idx_deliveries_message_id
  ON deliveries (message_id);

CREATE INDEX IF NOT EXISTS idx_events_message_delivery
  ON events (message_id, delivery_id);
`

func initSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("initialize schema: %w", err)
	}
	return nil
}
