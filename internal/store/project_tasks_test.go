package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"
)

func TestProjectTaskMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT, input_dir TEXT NOT NULL,
			output_path TEXT NOT NULL, started_at TEXT NOT NULL, completed_at TEXT,
			success INTEGER NOT NULL, error_message TEXT
		);
		CREATE TABLE task_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER NOT NULL REFERENCES tasks(id),
			created_at TEXT NOT NULL, message TEXT NOT NULL
		);
		INSERT INTO tasks VALUES (7, 'legacy input', 'legacy output', '2025-01-01T00:00:00Z', '2025-01-01T00:01:00Z', 0, 'legacy failure');
		INSERT INTO task_logs VALUES (9, 7, '2025-01-01T00:00:30Z', 'legacy log');
	`)
	closeErr := legacy.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("creating legacy database: %v, close: %v", err, closeErr)
	}
	for pass := 1; pass <= 2; pass++ {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer db.Close()
			rows, err := db.conn.Query(`PRAGMA table_info(tasks)`)
			if err != nil {
				t.Fatal(err)
			}
			columns := make(map[string]int)
			for rows.Next() {
				var cid, notNull, primaryKey int
				var name, kind string
				var defaultValue sql.NullString
				if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				columns[name] = notNull
			}
			rowsErr := rows.Err()
			rows.Close()
			if rowsErr != nil {
				t.Fatal(rowsErr)
			}
			for _, name := range []string{"project_id", "project_snapshot"} {
				if notNull, exists := columns[name]; !exists || notNull != 0 {
					t.Fatalf("migration pass %d: missing nullable %s column", pass, name)
				}
			}
			var projectID, snapshot sql.NullString
			if err := db.conn.QueryRow(`SELECT project_id, project_snapshot FROM tasks WHERE id = 7`).Scan(&projectID, &snapshot); err != nil {
				t.Fatal(err)
			}
			if projectID.Valid || snapshot.Valid {
				t.Fatal("legacy task acquired project fields")
			}
			history, err := db.GetHistory(0)
			if err != nil || len(history) != 1 {
				t.Fatalf("legacy history: %#v, %v", history, err)
			}
			e := history[0]
			if e.ID != 7 || e.InputDir != "legacy input" || e.OutputPath != "legacy output" || e.StartedAt != "2025-01-01T00:00:00Z" || e.CompletedAt == nil || *e.CompletedAt != "2025-01-01T00:01:00Z" || e.Success || e.ErrorMessage == nil || *e.ErrorMessage != "legacy failure" {
				t.Fatalf("legacy task changed: %#v", e)
			}
			logs, err := db.GetTaskLogs(7)
			if err != nil || !reflect.DeepEqual(logs, []LogLine{{CreatedAt: "2025-01-01T00:00:30Z", Message: "legacy log"}}) {
				t.Fatalf("legacy logs changed: %#v, %v", logs, err)
			}
		}()
	}
}

// Read the public JSON history representation, including the optional fields.
func projectHistoryFields(t *testing.T, entry TaskEntry) (id, snapshot *string) {
	t.Helper()
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		ProjectID       *string `json:"projectId"`
		ProjectSnapshot *string `json:"projectSnapshot"`
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields.ProjectID, entry.ProjectID) || !reflect.DeepEqual(fields.ProjectSnapshot, entry.ProjectSnapshot) {
		t.Fatal("history JSON does not expose optional project fields")
	}
	return fields.ProjectID, fields.ProjectSnapshot
}

func TestProjectTaskSnapshot(t *testing.T) {
	for _, name := range []string{"blank metadata with tags", "empty extra", "populated metadata", "empty collections", "nil collections"} {
		t.Run(name, func(t *testing.T) {
			db, path := openProjectDB(t)
			project := projectFixture(t)
			project.Sources[0].MTimeNS = 1_800_123_456_789_012_345
			project.Metadata.Title = ""
			switch name {
			case "empty extra":
				project.Metadata.Extra = map[string]string{}
			case "populated metadata":
				project.Metadata = meta.AudiobookMetadata{
					Title: "Title", Artist: "Artist", Album: "Album", AlbumArtist: "Album artist",
					Composer: "Composer", Genre: "Genre", Date: "2026", Copyright: "Copyright",
					Comment: "Comment", Description: "Description", Subtitle: "Subtitle", Grouping: "Grouping",
					Extra: map[string]string{"language": "pl", "series": ""},
				}
			case "empty collections":
				project.Sources = []book.Source{}
				project.Chapters = []book.Chapter{}
				project.Metadata.Extra = map[string]string{}
			case "nil collections":
				project.Sources = nil
				project.Chapters = nil
				project.Metadata.Extra = nil
			}
			if err := book.Validate(project); err != nil {
				t.Fatalf("invalid fixture: %v", err)
			}
			id, err := db.InsertProjectTask("project input", "project output", project)
			if err != nil || id <= 0 {
				t.Fatalf("insert = %d, %v", id, err)
			}
			history, err := db.GetHistory(1)
			if err != nil || len(history) != 1 {
				t.Fatalf("history = %#v, %v", history, err)
			}
			entry := history[0]
			if entry.ID != id || entry.InputDir != "project input" || entry.OutputPath != "project output" || entry.Success || entry.CompletedAt != nil || entry.ErrorMessage != nil {
				t.Fatalf("incorrect initial task: %#v", entry)
			}
			if _, err := time.Parse(time.RFC3339, entry.StartedAt); err != nil || !strings.HasSuffix(entry.StartedAt, "Z") {
				t.Fatalf("incorrect start time %q: %v", entry.StartedAt, err)
			}
			gotID, snapshot := projectHistoryFields(t, entry)
			if gotID == nil || *gotID != project.ID || snapshot == nil {
				t.Fatalf("missing snapshot fields: id=%v snapshot=%v", gotID, snapshot)
			}
			var stored book.BookProject
			if err := json.Unmarshal([]byte(*snapshot), &stored); err != nil || !reflect.DeepEqual(stored, project) {
				t.Fatalf("snapshot round trip: got %#v, want %#v, error %v", stored, project, err)
			}
			var snapshotFields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(*snapshot), &snapshotFields); err != nil {
				t.Fatal(err)
			}
			if string(snapshotFields["coverPath"]) != `""` {
				t.Fatal("literal blank cover omitted")
			}
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(snapshotFields["metadata"], &metadata); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"title", "artist", "album", "albumArtist", "composer", "genre", "date", "copyright", "comment", "description", "subtitle", "grouping", "extra"} {
				if _, exists := metadata[key]; !exists {
					t.Fatalf("metadata %s omitted", key)
				}
			}
			if len(project.Sources) > 0 {
				var sources []map[string]json.RawMessage
				if err := json.Unmarshal(snapshotFields["sources"], &sources); err != nil {
					t.Fatal(err)
				}
				if string(sources[0]["mtimeNs"]) != `"1800123456789012345"` {
					t.Fatalf("fingerprint lost string encoding: %s", sources[0]["mtimeNs"])
				}
				project.Sources[0].Path = "caller changed source"
				project.Sources[0].MTimeNS = 1
				project.Chapters[0].Title = "caller changed title"
				project.Chapters[0].Segments[0].StartUS = 42
			}
			if project.Metadata.Extra != nil {
				project.Metadata.Extra["series"] = "caller changed tag"
			}
			project.Metadata.Title = "caller changed metadata"
			project.Export.Format = "wav"
			if err := db.AppendLog(id, "project export failed"); err != nil {
				t.Fatal(err)
			}
			if err := db.CompleteTask(id, false, "export failure"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			history, err = reopened.GetHistory(1)
			if err != nil || len(history) != 1 {
				t.Fatalf("reopened history = %#v, %v", history, err)
			}
			entry = history[0]
			newID, newSnapshot := projectHistoryFields(t, entry)
			if newID == nil || *newID != *gotID || newSnapshot == nil || *newSnapshot != *snapshot {
				t.Fatal("persisted snapshot changed after caller mutation/completion/reopen")
			}
			if entry.CompletedAt == nil || entry.Success || entry.ErrorMessage == nil || *entry.ErrorMessage != "export failure" {
				t.Fatalf("completion incompatible: %#v", entry)
			}
			logs, err := reopened.GetTaskLogs(id)
			if err != nil || len(logs) != 1 || logs[0].Message != "project export failed" {
				t.Fatalf("logs incompatible: %#v, %v", logs, err)
			}
		})
	}
}

func TestProjectTaskLegacyInsert(t *testing.T) {
	db, _ := openProjectDB(t)
	id, err := db.InsertTask("old input", "old output")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendLog(id, "legacy new log"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(id, true, ""); err != nil {
		t.Fatal(err)
	}
	history, err := db.GetHistory(0)
	if err != nil || len(history) != 1 {
		t.Fatalf("legacy history = %#v, %v", history, err)
	}
	entry := history[0]
	projectID, snapshot := projectHistoryFields(t, entry)
	if entry.ID != id || entry.InputDir != "old input" || entry.OutputPath != "old output" || !entry.Success || entry.CompletedAt == nil || entry.ErrorMessage != nil || projectID != nil || snapshot != nil {
		t.Fatalf("legacy task incompatible: %#v", entry)
	}
	encoded, err := json.Marshal(entry)
	if err != nil || strings.Contains(string(encoded), `"projectId"`) || strings.Contains(string(encoded), `"projectSnapshot"`) {
		t.Fatalf("legacy optional fields not omitted: %s, %v", encoded, err)
	}
	var rowID, rowSnapshot sql.NullString
	if err := db.conn.QueryRow(`SELECT project_id, project_snapshot FROM tasks WHERE id = ?`, id).Scan(&rowID, &rowSnapshot); err != nil || rowID.Valid || rowSnapshot.Valid {
		t.Fatalf("legacy fields not SQL NULL: %v, %v, %v", rowID, rowSnapshot, err)
	}
	logs, err := db.GetTaskLogs(id)
	if err != nil || len(logs) != 1 || logs[0].Message != "legacy new log" {
		t.Fatalf("legacy logs incompatible: %#v, %v", logs, err)
	}
}

func TestProjectTaskInsertFailureIsAtomic(t *testing.T) {
	db, _ := openProjectDB(t)
	legacyID, err := db.InsertTask("existing input", "existing output")
	if err != nil {
		t.Fatal(err)
	}
	// Reject after SQLite has attempted the INSERT, so a two-step history write
	// cannot leave an ordinary task behind without its project snapshot.
	if _, err := db.conn.Exec(`CREATE TRIGGER reject_project_task AFTER INSERT ON tasks
		WHEN NEW.project_snapshot IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'forced project insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertProjectTask("project input", "project output", projectFixture(t))
	if err == nil || !strings.Contains(err.Error(), "forced project insert failure") || id != 0 {
		t.Fatalf("expected rejected insert and zero ID, got %d, %v", id, err)
	}
	history, err := db.GetHistory(0)
	if err != nil || len(history) != 1 || history[0].ID != legacyID {
		t.Fatalf("failed insert left partial task: %#v, %v", history, err)
	}
	if _, err := db.conn.Exec(`DROP TRIGGER reject_project_task`); err != nil {
		t.Fatal(err)
	}
	if id, err := db.InsertProjectTask("retry input", "retry output", projectFixture(t)); err != nil || id <= legacyID {
		t.Fatalf("connection unusable after rejected insert: %d, %v", id, err)
	}
}
