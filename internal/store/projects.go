package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"

	"github.com/google/uuid"
)

// ProjectSummary describes a saved project without loading its timeline.
type ProjectSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
}

// SaveProject stores an independent snapshot and a durable managed cover.
// Structural validation deliberately permits temporarily missing audio sources.
func (db *DB) SaveProject(project book.BookProject) (saveErr error) {
	if err := book.Validate(project); err != nil {
		return fmt.Errorf("saving project %q: %w", project.ID, err)
	}
	if err := uuid.Validate(project.ID); err != nil {
		return fmt.Errorf("saving project %q: invalid UUID: %w", project.ID, err)
	}
	snapshot := book.Clone(project)
	db.projectMu.Lock()
	defer db.projectMu.Unlock()

	if snapshot.CoverPath != "" {
		var priorJSON string
		err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, snapshot.ID).Scan(&priorJSON)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("preparing save for project %q: %w", snapshot.ID, err)
		}
		var prior book.BookProject
		// An invalid old snapshot may be replaced, but a valid cover reference
		// must survive rollback even when its file needed to be restored.
		if err == nil {
			_ = json.Unmarshal([]byte(priorJSON), &prior)
		}
		cover, created, err := publishProjectCover(snapshot.CoverPath, filepath.Join(db.projectRoot, snapshot.ID))
		if err != nil {
			return fmt.Errorf("saving project %q cover: %w", snapshot.ID, err)
		}
		snapshot.CoverPath = cover
		if created && canRemoveFailedCover(prior.CoverPath, cover) {
			defer func() {
				if saveErr != nil {
					// Rollback runs first; the serialized save still owns this
					// new file, and the unchanged prior row never referenced it.
					if err := os.Remove(cover); err != nil && !errors.Is(err, os.ErrNotExist) {
						saveErr = errors.Join(saveErr, fmt.Errorf("cleaning failed project %q cover: %w", snapshot.ID, err))
					}
				}
			}()
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encoding project %q JSON: %w", snapshot.ID, err)
	}
	connection, err := db.conn.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("reserving save connection for project %q: %w", snapshot.ID, err)
	}
	defer connection.Close()
	tx, err := connection.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("beginning save for project %q: %w", snapshot.ID, err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO book_projects (id, schema_version, title, project_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET schema_version = excluded.schema_version,
		title = excluded.title, project_json = excluded.project_json, updated_at = excluded.updated_at`,
		snapshot.ID, snapshot.SchemaVersion, snapshot.Metadata.Title, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("saving project %q: %w", snapshot.ID, err)
	}
	if err := tx.Commit(); err != nil {
		// database/sql marks Tx done before invoking the driver, so its
		// deferred Rollback cannot end a native transaction after COMMIT fails.
		// Keep this connection reserved until SQLite rolls back or it is discarded.
		if _, rollbackErr := connection.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil {
			_ = connection.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("rolling back failed commit: %w", rollbackErr))
		}
		return fmt.Errorf("committing project %q: %w", snapshot.ID, err)
	}
	return nil
}

// canRemoveFailedCover compares existing file identity after publication, so a
// restored cover remains protected through a canonical path or directory alias.
// If identity cannot be checked, retain the file rather than risk an old reference.
func canRemoveFailedCover(previous, current string) bool {
	if previous == current {
		return false
	}
	if previous == "" {
		return true
	}
	priorInfo, err := os.Stat(previous)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	currentInfo, err := os.Stat(current)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return !os.SameFile(priorInfo, currentInfo)
}

// publishProjectCover stages through the shared image validator, then uses an
// exclusive link to identify exactly which durable file this save created.
func publishProjectCover(imagePath, projectDir string) (path string, created bool, err error) {
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return "", false, err
	}
	staging, err := os.MkdirTemp(projectDir, ".save-cover-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(staging)
	staged, err := meta.CopyCoverToCache(imagePath, staging)
	if err != nil {
		return "", false, err
	}
	destination := filepath.Join(projectDir, filepath.Base(staged))
	if err := os.Link(staged, destination); err == nil {
		return destination, true, nil
	} else if !errors.Is(err, os.ErrExist) {
		return "", false, fmt.Errorf("publishing durable cover: %w", err)
	}
	// Reuse only a regular existing file with the correct content.
	if _, err := meta.CopyCoverToCache(staged, projectDir); err != nil {
		return "", false, err
	}
	// Both branches retain the requested absolute DB-root representation.
	return destination, false, nil
}

// GetProject loads a structurally valid snapshot without rediscovering metadata
// or requiring its audio sources to be currently available.
func (db *DB) GetProject(id string) (book.BookProject, error) {
	var rowID, encoded string
	var version int
	if err := db.conn.QueryRow(`SELECT id, schema_version, project_json FROM book_projects WHERE id = ?`, id).Scan(&rowID, &version, &encoded); err != nil {
		return book.BookProject{}, fmt.Errorf("loading project %q: %w", id, err)
	}
	if version != 1 {
		return book.BookProject{}, fmt.Errorf("loading project %q: unsupported schema version %d", id, version)
	}
	var project book.BookProject
	if err := json.Unmarshal([]byte(encoded), &project); err != nil {
		return book.BookProject{}, fmt.Errorf("decoding project %q JSON: %w", id, err)
	}
	if project.ID != rowID {
		return book.BookProject{}, fmt.Errorf("loading project %q: row and JSON ID disagree (%q)", id, project.ID)
	}
	if project.SchemaVersion != version {
		return book.BookProject{}, fmt.Errorf("loading project %q: row and JSON schema version disagree (%d)", id, project.SchemaVersion)
	}
	if err := book.Validate(project); err != nil {
		return book.BookProject{}, fmt.Errorf("validating loaded project %q: %w", id, err)
	}
	return project, nil
}

// ListProjects returns the most recently updated projects, with stable ID ties.
func (db *DB) ListProjects(limit int) ([]ProjectSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.conn.Query(`SELECT id, title, updated_at FROM book_projects ORDER BY updated_at DESC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	defer rows.Close()
	projects := make([]ProjectSummary, 0)
	for rows.Next() {
		var project ProjectSummary
		if err := rows.Scan(&project.ID, &project.Title, &project.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning project summary: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return projects, nil
}
