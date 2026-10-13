package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"chaptersmith/internal/book"
)

// InsertProjectTask atomically records a task and its complete export snapshot.
// Encoding before the INSERT detaches persisted bytes from caller-owned data.
func (db *DB) InsertProjectTask(inputDir, outputPath string, project book.BookProject) (int64, error) {
	encoded, err := json.Marshal(project)
	if err != nil {
		return 0, fmt.Errorf("encoding task project %q JSON: %w", project.ID, err)
	}
	res, err := db.conn.Exec(
		`INSERT INTO tasks (input_dir, output_path, started_at, completed_at, success, error_message, project_id, project_snapshot)
		VALUES (?, ?, ?, NULL, 0, NULL, ?, ?)`,
		inputDir, outputPath, time.Now().UTC().Format(time.RFC3339), project.ID, string(encoded),
	)
	if err != nil {
		return 0, fmt.Errorf("inserting project task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("getting last insert id: %w", err)
	}
	return id, nil
}

// migrateProjectTasks also upgrades databases created before project exports.
func migrateProjectTasks(conn *sql.DB) error {
	rows, err := conn.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		return fmt.Errorf("reading task columns: %w", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("reading task column: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading task columns: %w", err)
	}
	// Release the cursor before DDL on the single pooled connection.
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing task columns: %w", err)
	}
	for _, name := range []string{"project_id", "project_snapshot"} {
		if !columns[name] {
			if _, err := conn.Exec(`ALTER TABLE tasks ADD COLUMN ` + name + ` TEXT`); err != nil {
				return fmt.Errorf("adding task column %s: %w", name, err)
			}
		}
	}
	return nil
}
