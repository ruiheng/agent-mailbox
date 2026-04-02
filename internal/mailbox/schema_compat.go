package mailbox

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateLegacyGroupMessageSchema(ctx context.Context, db *sql.DB) error {
	hasLegacyKey, err := tableHasColumn(ctx, db, "group_messages", "group_message_id")
	if err != nil {
		return fmt.Errorf("inspect legacy group_messages schema: %w", err)
	}
	if !hasLegacyKey {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy group schema migration: %w", err)
	}
	defer tx.Rollback()

	statements := []string{
		`DROP INDEX IF EXISTS idx_group_reads_person_message`,
		`DROP INDEX IF EXISTS idx_group_message_eligibility_person_message`,
		`DROP INDEX IF EXISTS idx_group_messages_group_created`,
		`DROP TABLE IF EXISTS group_reads`,
		`DROP TABLE IF EXISTS group_message_eligibility`,
		`DROP TABLE IF EXISTS group_messages`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply legacy group schema migration: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy group schema migration: %w", err)
	}
	return nil
}

func tableHasColumn(ctx context.Context, db *sql.DB, tableName, columnName string) (bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}
