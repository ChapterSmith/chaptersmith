package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  input_dir TEXT NOT NULL,
  output_path TEXT NOT NULL,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  success INTEGER NOT NULL,
  error_message TEXT
);

CREATE TABLE IF NOT EXISTS task_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id),
  created_at TEXT NOT NULL,
  message TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_logs_task_id ON task_logs(task_id);

CREATE TABLE IF NOT EXISTS book_projects (
  id TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  title TEXT NOT NULL,
  project_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`

// DB wraps the SQLite connection and provides task/history and project operations.
type DB struct {
	conn        *sql.DB
	projectRoot string
	projectMu   sync.Mutex
}

// TaskEntry represents a completed or in-progress merge task.
type TaskEntry struct {
	ID              int64   `json:"id"`
	InputDir        string  `json:"inputDir"`
	OutputPath      string  `json:"outputPath"`
	StartedAt       string  `json:"startedAt"`
	CompletedAt     *string `json:"completedAt,omitempty"`
	Success         bool    `json:"success"`
	ErrorMessage    *string `json:"errorMessage,omitempty"`
	ProjectID       *string `json:"projectId,omitempty"`
	ProjectSnapshot *string `json:"projectSnapshot,omitempty"`
}

// LogLine represents a single log line for a task.
type LogLine struct {
	CreatedAt string `json:"createdAt"`
	Message   string `json:"message"`
}

// Open opens or creates the database at dbPath and runs migrations.
func Open(dbPath string) (*DB, error) {
	absolutePath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolving db path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	// Queue local readers and writers on one physical connection. SQLite's
	// rollback journal otherwise lets an active reader block another pooled
	// connection's COMMIT, leaving that native transaction locked on failure.
	conn.SetMaxOpenConns(1)

	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	if err := migrateProjectTasks(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &DB{conn: conn, projectRoot: filepath.Join(filepath.Dir(absolutePath), "projects")}, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// InsertTask creates a new task and returns its ID.
func (db *DB) InsertTask(inputDir, outputPath string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.conn.Exec(
		`INSERT INTO tasks (input_dir, output_path, started_at, completed_at, success, error_message) VALUES (?, ?, ?, NULL, 0, NULL)`,
		inputDir, outputPath, now,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("getting last insert id: %w", err)
	}
	return id, nil
}

// CompleteTask marks a task as finished.
func (db *DB) CompleteTask(id int64, success bool, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	successInt := 0
	if success {
		successInt = 1
	}
	var errMsgVal any
	if errMsg != "" {
		errMsgVal = errMsg
	}
	_, err := db.conn.Exec(
		`UPDATE tasks SET completed_at = ?, success = ?, error_message = ? WHERE id = ?`,
		now, successInt, errMsgVal, id,
	)
	return err
}

// AppendLog adds a log line for a task.
func (db *DB) AppendLog(taskID int64, message string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.conn.Exec(
		`INSERT INTO task_logs (task_id, created_at, message) VALUES (?, ?, ?)`,
		taskID, now, message,
	)
	return err
}

// GetHistory returns the most recent tasks, limited by limit.
func (db *DB) GetHistory(limit int) ([]TaskEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.conn.Query(
		`SELECT id, input_dir, output_path, started_at, completed_at, success, error_message, project_id, project_snapshot FROM tasks ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying history: %w", err)
	}
	defer rows.Close()

	var entries []TaskEntry
	for rows.Next() {
		var e TaskEntry
		var completedAt, errorMsg, projectID, projectSnapshot sql.NullString
		var successInt int
		err := rows.Scan(&e.ID, &e.InputDir, &e.OutputPath, &e.StartedAt, &completedAt, &successInt, &errorMsg, &projectID, &projectSnapshot)
		if err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		if completedAt.Valid {
			e.CompletedAt = &completedAt.String
		}
		if errorMsg.Valid && errorMsg.String != "" {
			e.ErrorMessage = &errorMsg.String
		}
		if projectID.Valid {
			e.ProjectID = &projectID.String
		}
		if projectSnapshot.Valid {
			e.ProjectSnapshot = &projectSnapshot.String
		}
		e.Success = successInt == 1
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// GetTaskLogs returns log lines for a task.
func (db *DB) GetTaskLogs(taskID int64) ([]LogLine, error) {
	rows, err := db.conn.Query(
		`SELECT created_at, message FROM task_logs WHERE task_id = ? ORDER BY id ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying logs: %w", err)
	}
	defer rows.Close()

	var lines []LogLine
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.CreatedAt, &l.Message); err != nil {
			return nil, fmt.Errorf("scanning log row: %w", err)
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}
