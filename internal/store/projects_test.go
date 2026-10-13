package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"
)

const projectID = "00000000-0000-4000-8000-000000000001"

func openProjectDB(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func projectFixture(t *testing.T) book.BookProject {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.wav")
	if err := os.WriteFile(path, []byte("immutable source"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return book.BookProject{
		ID: projectID, SchemaVersion: 1,
		Sources: []book.Source{{ID: "source", Path: path, DurationUS: 3_000_000, SizeBytes: st.Size(), MTimeNS: st.ModTime().UnixNano(), SampleRate: 44100, Channels: 2}},
		Chapters: []book.Chapter{
			{ID: "moved", Title: "Second first", Segments: []book.Segment{{SourceID: "source", StartUS: 1_250_000, EndUS: 3_000_000}, {SilenceUS: 125_001}}},
			{ID: "split", Title: "First second", Segments: []book.Segment{{SourceID: "source", EndUS: 500_000}, {SourceID: "source", StartUS: 500_000, EndUS: 1_250_000}}},
		},
		Metadata: meta.AudiobookMetadata{Title: "Saved title", Extra: map[string]string{"unknown:tag": "preserved", "series": ""}},
		Export:   book.ExportSettings{Profile: "high", Channels: "mono", Normalize: true, OutputMode: "both", Format: "flac"},
	}
}

func writeProjectCover(t *testing.T, shade uint8) (string, []byte) {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, color.RGBA{R: shade, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, im); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cover.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path, encoded.Bytes()
}

func mustSaveProject(t *testing.T, db *DB, project book.BookProject) {
	t.Helper()
	if err := db.SaveProject(project); err != nil {
		t.Fatal(err)
	}
}

func mustGetProject(t *testing.T, db *DB, id string) book.BookProject {
	t.Helper()
	p, err := db.GetProject(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectRoundTrip(t *testing.T) {
	db, path := openProjectDB(t)
	p := projectFixture(t)
	var cover []byte
	p.CoverPath, cover = writeProjectCover(t, 23)
	sourceBytes, err := os.ReadFile(p.Sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	mustSaveProject(t, db, p)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := mustGetProject(t, reopened, p.ID)
	sum := sha256.Sum256(cover)
	wantCover := filepath.Join(filepath.Dir(path), "projects", p.ID, fmt.Sprintf("%x.png", sum))
	if got.CoverPath != wantCover {
		t.Fatalf("durable cover: got %s, want %s", got.CoverPath, wantCover)
	}
	if err := os.Remove(p.CoverPath); err != nil {
		t.Fatal(err)
	}
	durableBytes, err := os.ReadFile(got.CoverPath)
	if err != nil || !bytes.Equal(durableBytes, cover) {
		t.Fatalf("durable bytes: %v", err)
	}
	got.CoverPath = p.CoverPath
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip:\ngot %#v\nwant %#v", got, p)
	}
	unchanged, err := os.ReadFile(p.Sources[0].Path)
	if err != nil || !bytes.Equal(unchanged, sourceBytes) {
		t.Fatalf("source changed: %v", err)
	}
}

func TestSaveDoesNotMutateCaller(t *testing.T) {
	db, _ := openProjectDB(t)
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 24)
	// JSON provides an independent snapshot of all caller-owned slices and maps.
	before, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	mustSaveProject(t, db, p)
	after, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("caller mutated: before %s, after %s", before, after)
	}
	p.Metadata.Extra["unknown:tag"] = "caller later edit"
	p.Chapters[0].Segments[0].StartUS = 1_500_000
	got := mustGetProject(t, db, p.ID)
	if got.Metadata.Extra["unknown:tag"] != "preserved" || got.Chapters[0].Segments[0].StartUS != 1_250_000 {
		t.Fatal("saved snapshot followed caller changes")
	}
}

func TestClearedMetadataAndCoverStayEmpty(t *testing.T) {
	db, path := openProjectDB(t)
	p := projectFixture(t)
	p.Metadata = meta.AudiobookMetadata{Extra: map[string]string{}}
	// Tagged fixture and a source-side cover must never repopulate saved fields.
	tagged, err := os.ReadFile(filepath.Join("..", "..", "testdata", "formats", "m4a", "01.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Sources[0].Path, tagged, 0600); err != nil {
		t.Fatal(err)
	}
	_, sidecar := writeProjectCover(t, 25)
	sidecarPath := filepath.Join(filepath.Dir(p.Sources[0].Path), "cover.jpg")
	if err := os.WriteFile(sidecarPath, sidecar, 0600); err != nil {
		t.Fatal(err)
	}
	mustSaveProject(t, db, p)
	db.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := mustGetProject(t, reopened, p.ID)
	if !reflect.DeepEqual(got.Metadata, p.Metadata) || got.CoverPath != "" {
		t.Fatalf("cleared values repopulated: %#v", got)
	}
	if _, err := os.Stat(sidecarPath); err != nil {
		t.Fatal("source-side cover removed:", err)
	}
}

func TestMissingSourceCanReopen(t *testing.T) {
	db, path := openProjectDB(t)
	p := projectFixture(t)
	mustSaveProject(t, db, p)
	if err := os.Remove(p.Sources[0].Path); err != nil {
		t.Fatal(err)
	}
	// Saving again also requires only structural validation.
	mustSaveProject(t, db, p)
	db.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got := mustGetProject(t, reopened, p.ID)
	if err := book.CheckSources(got); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source not reported: %v", err)
	}
}

func TestListProjectsAndHistory(t *testing.T) {
	db, _ := openProjectDB(t)
	empty, err := db.ListProjects(0)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v, %v", empty, err)
	}
	task, err := db.InsertTask("input", "output")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendLog(task, "still here"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(task, true, ""); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetHistory(0)
	if err != nil {
		t.Fatal(err)
	}
	p := projectFixture(t)
	for i := 1; i <= 53; i++ {
		p.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		p.Metadata.Title = fmt.Sprintf("Book %d", i)
		mustSaveProject(t, db, p)
	}
	all, err := db.ListProjects(100)
	if err != nil || len(all) != 53 {
		t.Fatalf("all = %d, %v", len(all), err)
	}
	for _, summary := range all {
		stamp, err := time.Parse(time.RFC3339Nano, summary.UpdatedAt)
		if err != nil || !strings.HasSuffix(summary.UpdatedAt, "Z") || stamp.Location() != time.UTC {
			t.Fatalf("UTC timestamp %q: %v", summary.UpdatedAt, err)
		}
	}
	for _, limit := range []int{0, -1} {
		list, err := db.ListProjects(limit)
		if err != nil || len(list) != 50 {
			t.Fatalf("default limit %d = %d, %v", limit, len(list), err)
		}
	}
	// Establish explicit timestamps to test ordering independently of wall-clock timing.
	if _, err := db.conn.Exec(`UPDATE book_projects SET updated_at = '2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`UPDATE book_projects SET updated_at = '2026-01-02T00:00:00Z' WHERE id = ?`, p.ID); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListProjects(3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{p.ID, projectID, "00000000-0000-4000-8000-000000000002"}
	for i := range want {
		if list[i].ID != want[i] {
			t.Fatalf("ordering = %#v", list)
		}
	}
	if list[0].Title != "Book 53" {
		t.Fatalf("summary title = %q", list[0].Title)
	}
	encoded, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["id"] != p.ID || fields["title"] != "Book 53" || fields["updatedAt"] != "2026-01-02T00:00:00Z" {
		t.Fatalf("summary JSON = %s", encoded)
	}
	after, err := db.GetHistory(0)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("history changed: %#v, %v", after, err)
	}
	logs, err := db.GetTaskLogs(task)
	if err != nil || len(logs) != 1 || logs[0].Message != "still here" {
		t.Fatalf("logs changed: %#v, %v", logs, err)
	}
}

func TestUnknownProject(t *testing.T) {
	db, _ := openProjectDB(t)
	_, err := db.GetProject("00000000-0000-4000-8000-000000000000")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wanted sql.ErrNoRows, got %v", err)
	}
}

func TestUnknownOrCorruptProject(t *testing.T) {
	db, _ := openProjectDB(t)
	p := projectFixture(t)
	for _, test := range []struct {
		name       string
		rowVersion int
		alter      func(*book.BookProject)
		raw        string
		want       string
	}{
		{name: "unknown row schema", rowVersion: 2, want: "schema"},
		{name: "malformed JSON", rowVersion: 1, raw: "{broken", want: "JSON"},
		{name: "mismatched ID", rowVersion: 1, alter: func(p *book.BookProject) { p.ID = "00000000-0000-4000-8000-000000000099" }, want: "ID"},
		{name: "mismatched version", rowVersion: 1, alter: func(p *book.BookProject) { p.SchemaVersion = 2 }, want: "version"},
		{name: "invalid timeline", rowVersion: 1, alter: func(p *book.BookProject) { p.Chapters[0].Segments[0].EndUS = 9_000_000 }, want: "interval"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mustSaveProject(t, db, p)
			bad := book.Clone(p)
			if test.alter != nil {
				test.alter(&bad)
			}
			encoded, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if test.raw != "" {
				encoded = []byte(test.raw)
			}
			if _, err := db.conn.Exec(`UPDATE book_projects SET schema_version = ?, project_json = ? WHERE id = ?`, test.rowVersion, string(encoded), p.ID); err != nil {
				t.Fatal(err)
			}
			_, err = db.GetProject(p.ID)
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), p.ID) {
				t.Fatalf("wanted contextual %q error, got %v", test.want, err)
			}
		})
	}
}

func TestSaveRejectsInvalidProjectBeforePublication(t *testing.T) {
	for _, invalid := range []string{"UUID", "schema", "timeline"} {
		t.Run(invalid, func(t *testing.T) {
			db, path := openProjectDB(t)
			p := projectFixture(t)
			p.CoverPath, _ = writeProjectCover(t, 26)
			switch invalid {
			case "UUID":
				p.ID = "../../escape"
			case "schema":
				p.SchemaVersion = 2
			case "timeline":
				p.Chapters[0].Segments[0].StartUS = -1
			}
			if err := db.SaveProject(p); err == nil || !strings.Contains(err.Error(), p.ID) {
				t.Fatalf("invalid save = %v", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "projects")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cover published before validation: %v", err)
			}
			list, err := db.ListProjects(0)
			if err != nil || len(list) != 0 {
				t.Fatalf("invalid project persisted: %#v, %v", list, err)
			}
		})
	}
}

func TestSaveAtomicFailure(t *testing.T) {
	db, path := openProjectDB(t)
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 27)
	mustSaveProject(t, db, p)
	old := mustGetProject(t, db, p.ID)
	var oldJSON string
	if err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID).Scan(&oldJSON); err != nil {
		t.Fatal(err)
	}
	assertOld := func(t *testing.T) {
		t.Helper()
		var gotJSON string
		if err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID).Scan(&gotJSON); err != nil {
			t.Fatal(err)
		}
		if gotJSON != oldJSON {
			t.Fatal("failed save replaced old JSON")
		}
		if _, err := os.ReadFile(old.CoverPath); err != nil {
			t.Fatal("old cover lost:", err)
		}
		if got := mustGetProject(t, db, p.ID); !reflect.DeepEqual(got, old) {
			t.Fatal("old snapshot changed")
		}
	}
	if _, err := db.conn.Exec(`CREATE TEMP TRIGGER reject_project BEFORE UPDATE ON book_projects BEGIN SELECT RAISE(ABORT, 'forced upsert failure'); END`); err != nil {
		t.Fatal(err)
	}
	// TEMP triggers belong to a connection: retain that connection for this fault injection.
	db.conn.SetMaxOpenConns(1)
	newPath, newBytes := writeProjectCover(t, 28)
	next := book.Clone(p)
	next.CoverPath = newPath
	next.Metadata.Title = "must not persist"
	if err := db.SaveProject(next); err == nil || !strings.Contains(err.Error(), p.ID) {
		t.Fatalf("trigger save = %v", err)
	}
	assertOld(t)
	sum := sha256.Sum256(newBytes)
	newDurable := filepath.Join(filepath.Dir(path), "projects", p.ID, fmt.Sprintf("%x.png", sum))
	if _, err := os.Stat(newDurable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed-save new cover not cleaned: %v", err)
	}
	// A reused cover is never owned by a failed save.
	if err := db.SaveProject(old); err == nil {
		t.Fatal("expected trigger failure on reused cover")
	}
	assertOld(t)
	// If a previously referenced durable file was missing, restoring it must
	// survive failure too: the unchanged row still references that pathname.
	if err := os.Remove(old.CoverPath); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProject(p); err == nil {
		t.Fatal("expected trigger failure after restoring the old cover")
	}
	assertOld(t)
	if err := os.WriteFile(newDurable, newBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProject(next); err == nil {
		t.Fatal("expected trigger failure on existing new cover")
	}
	if _, err := os.Stat(newDurable); err != nil {
		t.Fatal("preexisting unreferenced cover deleted:", err)
	}
	assertOld(t)
	if _, err := db.conn.Exec(`DROP TRIGGER reject_project`); err != nil {
		t.Fatal(err)
	}
	next.CoverPath = filepath.Join(t.TempDir(), "missing.png")
	if err := db.SaveProject(next); err == nil {
		t.Fatal("missing source cover accepted")
	}
	assertOld(t)
	if err := os.Remove(newDurable); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newDurable, 0700); err != nil {
		t.Fatal(err)
	}
	next.CoverPath = newPath
	if err := db.SaveProject(next); err == nil {
		t.Fatal("publication obstruction accepted")
	}
	assertOld(t)
}

func TestConcurrentProjectSaves(t *testing.T) {
	db, _ := openProjectDB(t)
	base := projectFixture(t)
	a, aBytes := writeProjectCover(t, 30)
	b, bBytes := writeProjectCover(t, 31)
	failedCover, _ := writeProjectCover(t, 33)
	base.CoverPath = a
	mustSaveProject(t, db, base)
	if _, err := db.conn.Exec(`CREATE TRIGGER reject_failed_snapshot BEFORE UPDATE ON book_projects WHEN NEW.title = 'fail' BEGIN SELECT RAISE(ABORT, 'forced concurrent failure'); END`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 120)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < 12; iteration++ {
				p := book.Clone(base)
				if worker%2 == 0 {
					p.Metadata.Title = "A"
					p.CoverPath = a
					p.Metadata.Extra["snapshot"] = "A"
				} else {
					p.Metadata.Title = "B"
					p.CoverPath = b
					p.Metadata.Extra["snapshot"] = "B"
				}
				if err := db.SaveProject(p); err != nil {
					failures <- err
					return
				}
				got, err := db.GetProject(p.ID)
				if err != nil {
					failures <- err
					return
				}
				if err := book.Validate(got); err != nil {
					failures <- err
					return
				}
				if got.Metadata.Title != got.Metadata.Extra["snapshot"] {
					failures <- fmt.Errorf("torn snapshot: %#v", got.Metadata)
					return
				}
				if _, err := os.ReadFile(got.CoverPath); err != nil {
					failures <- err
					return
				}
				wantBytes := aBytes
				if got.Metadata.Title == "B" {
					wantBytes = bBytes
				}
				coverBytes, err := os.ReadFile(got.CoverPath)
				if err != nil || !bytes.Equal(coverBytes, wantBytes) {
					failures <- fmt.Errorf("cover differs from committed snapshot %s: %v", got.Metadata.Title, err)
					return
				}
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for iteration := 0; iteration < 12; iteration++ {
			p := book.Clone(base)
			p.Metadata.Title = "fail"
			p.CoverPath = failedCover
			// Alternate a new unreferenced cover and a reused successful cover.
			if iteration%2 == 0 {
				p.CoverPath = b
			}
			if err := db.SaveProject(p); err == nil {
				failures <- fmt.Errorf("concurrent rejected snapshot unexpectedly persisted")
				return
			}
		}
	}()
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestSaveClosedDatabaseDoesNotPublishCover(t *testing.T) {
	db, path := openProjectDB(t)
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 34)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveProject(p); err == nil || !strings.Contains(err.Error(), p.ID) {
		t.Fatalf("closed DB save = %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "projects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed database published a cover: %v", err)
	}
}

func TestSaveKeepsOlderCoverVersions(t *testing.T) {
	db, _ := openProjectDB(t)
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 35)
	mustSaveProject(t, db, p)
	old := mustGetProject(t, db, p.ID)
	p.CoverPath, _ = writeProjectCover(t, 36)
	mustSaveProject(t, db, p)
	newer := mustGetProject(t, db, p.ID)
	p.CoverPath = ""
	mustSaveProject(t, db, p)
	if got := mustGetProject(t, db, p.ID); got.CoverPath != "" {
		t.Fatal("cover clear did not persist")
	}
	for _, cover := range []string{old.CoverPath, newer.CoverPath} {
		if _, err := os.ReadFile(cover); err != nil {
			t.Fatalf("older cover version lost: %v", err)
		}
	}
}

func TestProjectRootFromRelativeDatabasePath(t *testing.T) {
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(cwd, "nested", "history.db")
	relative, err := filepath.Rel(cwd, absolute)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(relative)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 32)
	mustSaveProject(t, db, p)
	got := mustGetProject(t, db, p.ID)
	if !filepath.IsAbs(got.CoverPath) || filepath.Dir(filepath.Dir(got.CoverPath)) != filepath.Join(filepath.Dir(absolute), "projects") {
		t.Fatalf("incorrect relative DB cover root: %s", got.CoverPath)
	}
}

func TestSaveAtomicFailureWithSymlinkDatabaseDirectory(t *testing.T) {
	for _, canonicalPrior := range []bool{false, true} {
		name := "successful reuse"
		if canonicalPrior {
			name = "prior canonical path"
		}
		t.Run(name, func(t *testing.T) {
			realDirectory := t.TempDir()
			alias := filepath.Join(t.TempDir(), "db-alias")
			if err := os.Symlink(realDirectory, alias); err != nil {
				t.Skipf("symlink unsupported: %v", err)
			}
			db, err := Open(filepath.Join(alias, "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			p := projectFixture(t)
			var coverBytes []byte
			p.CoverPath, coverBytes = writeProjectCover(t, 37)
			before, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			mustSaveProject(t, db, p)
			first := mustGetProject(t, db, p.ID)
			mustSaveProject(t, db, p)
			old := mustGetProject(t, db, p.ID)
			if old.CoverPath != first.CoverPath {
				t.Errorf("successful reuse changed durable path: %q -> %q", first.CoverPath, old.CoverPath)
			}
			if canonicalPrior {
				// Older snapshots may use the shared helper's canonical path.
				old.CoverPath, err = filepath.EvalSymlinks(old.CoverPath)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.conn.Exec(`UPDATE book_projects SET project_json = ? WHERE id = ?`, string(encoded), p.ID); err != nil {
					t.Fatal(err)
				}
			}
			var oldJSON string
			if err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID).Scan(&oldJSON); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(old.CoverPath); err != nil {
				t.Fatal(err)
			}
			if _, err := db.conn.Exec(`CREATE TRIGGER reject_symlink_save BEFORE UPDATE ON book_projects BEGIN SELECT RAISE(ABORT, 'forced symlink save failure'); END`); err != nil {
				t.Fatal(err)
			}
			if err := db.SaveProject(p); err == nil || !strings.Contains(err.Error(), "forced symlink save failure") {
				t.Fatalf("wanted forced SQL error after cover restoration, got %v", err)
			}
			var gotJSON string
			if err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID).Scan(&gotJSON); err != nil {
				t.Fatal(err)
			}
			if gotJSON != oldJSON || !reflect.DeepEqual(mustGetProject(t, db, p.ID), old) {
				t.Fatal("rejected save changed previous snapshot")
			}
			restored, err := os.ReadFile(old.CoverPath)
			if err != nil || !bytes.Equal(restored, coverBytes) {
				t.Fatalf("previously referenced cover lost after rejected symlink save: %v", err)
			}
			after, err := json.Marshal(p)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("caller mutated during save/reuse/rejected restoration: %v", err)
			}
			originalCover, err := os.ReadFile(p.CoverPath)
			if err != nil || !bytes.Equal(originalCover, coverBytes) {
				t.Fatalf("original cover changed: %v", err)
			}
		})
	}
}

func TestSaveWaitsForActiveProjectReader(t *testing.T) {
	db, _ := openProjectDB(t)
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 38)
	mustSaveProject(t, db, p)
	previous := mustGetProject(t, db, p.ID)

	// Keep a real SQLite read cursor open after its first row, holding its
	// snapshot while another goroutine attempts a transactional project save.
	rows, err := db.conn.Query(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("reader did not obtain saved snapshot: %v", rows.Err())
	}
	var readJSON string
	if err := rows.Scan(&readJSON); err != nil {
		t.Fatal(err)
	}
	var readProject book.BookProject
	if err := json.Unmarshal([]byte(readJSON), &readProject); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readProject, previous) {
		t.Fatal("reader did not obtain whole previous snapshot")
	}

	p.Metadata.Title = "Updated after active reader"
	var newCoverBytes []byte
	p.CoverPath, newCoverBytes = writeProjectCover(t, 39)
	callerBefore, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	queuedBefore := db.conn.Stats().WaitCount
	saved := make(chan error, 1)
	go func() { saved <- db.SaveProject(p) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var saveErr error
	completed := false
	waiting := false
	// Wait for the save to reach contention, not for an arbitrary sleep.
	// With one connection its SQL request queues behind the active cursor;
	// the old pool instead finishes immediately with SQLITE_BUSY at COMMIT.
	for !completed && !waiting {
		select {
		case saveErr = <-saved:
			completed = true
		case <-deadline.C:
			t.Fatal("save neither completed nor queued behind the active reader")
		default:
			waiting = db.conn.Stats().WaitCount > queuedBefore
			runtime.Gosched()
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !completed {
		select {
		case saveErr = <-saved:
		case <-deadline.C:
			t.Fatal("save did not complete after releasing the active reader")
		}
	}
	if saveErr != nil {
		t.Fatalf("save contending with active project reader: %v", saveErr)
	}
	got := mustGetProject(t, db, p.ID)
	want := book.Clone(p)
	want.CoverPath = got.CoverPath
	if !reflect.DeepEqual(got, want) {
		t.Fatal("contended save did not commit a whole new snapshot")
	}
	cover, err := os.ReadFile(got.CoverPath)
	if err != nil || !bytes.Equal(cover, newCoverBytes) {
		t.Fatalf("new committed cover unavailable: %v", err)
	}
	if _, err := os.ReadFile(previous.CoverPath); err != nil {
		t.Fatalf("previous snapshot cover removed: %v", err)
	}
	callerAfter, err := json.Marshal(p)
	if err != nil || !bytes.Equal(callerBefore, callerAfter) {
		t.Fatalf("caller mutated during contended save: %v", err)
	}
	// Subsequent project/history reads and writes must remain usable rather
	// than inheriting a native transaction left in the connection pool.
	task, err := db.InsertTask("reader contention", "output")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendLog(task, "saved after reader closed"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(task, true, ""); err != nil {
		t.Fatal(err)
	}
	history, err := db.GetHistory(1)
	if err != nil || len(history) != 1 || history[0].ID != task || !history[0].Success {
		t.Fatalf("history unusable after contention: %#v, %v", history, err)
	}
	logs, err := db.GetTaskLogs(task)
	if err != nil || len(logs) != 1 || logs[0].Message != "saved after reader closed" {
		t.Fatalf("task logs unusable after contention: %#v, %v", logs, err)
	}
}

func TestSaveCommitFailureRestoresSnapshotAndConnection(t *testing.T) {
	db, path := openProjectDB(t)
	p := projectFixture(t)
	p.CoverPath, _ = writeProjectCover(t, 40)
	mustSaveProject(t, db, p)
	previous := mustGetProject(t, db, p.ID)
	var oldJSON string
	if err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID).Scan(&oldJSON); err != nil {
		t.Fatal(err)
	}

	// An independent connection holds a real read lock that allows the save's
	// UPDATE but deterministically prevents SQLite's COMMIT from succeeding.
	external, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	reader, err := external.Query(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if !reader.Next() {
		t.Fatalf("external reader did not obtain snapshot: %v", reader.Err())
	}
	var externalJSON string
	if err := reader.Scan(&externalJSON); err != nil || externalJSON != oldJSON {
		t.Fatalf("external reader snapshot: %v", err)
	}
	next := book.Clone(p)
	next.Metadata.Title = "Rejected at commit"
	var newBytes []byte
	next.CoverPath, newBytes = writeProjectCover(t, 41)
	callerBefore, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	err = db.SaveProject(next)
	if err == nil || !strings.Contains(err.Error(), "committing project") || !strings.Contains(err.Error(), "SQLITE_BUSY") {
		t.Fatalf("expected deterministic commit lock error, got %v", err)
	}
	var currentJSON string
	if err := db.conn.QueryRow(`SELECT project_json FROM book_projects WHERE id = ?`, p.ID).Scan(&currentJSON); err != nil {
		t.Fatal(err)
	}
	if currentJSON != oldJSON {
		t.Error("failed COMMIT left changed project JSON visible on pooled connection")
	}
	if got := mustGetProject(t, db, p.ID); !reflect.DeepEqual(got, previous) {
		t.Errorf("failed COMMIT did not restore previous snapshot: title=%q cover=%q", got.Metadata.Title, got.CoverPath)
	}
	if _, err := os.ReadFile(previous.CoverPath); err != nil {
		t.Fatalf("previous cover lost after failed COMMIT: %v", err)
	}
	newHash := sha256.Sum256(newBytes)
	newDurable := filepath.Join(filepath.Dir(previous.CoverPath), fmt.Sprintf("%x.png", newHash))
	if _, err := os.Stat(newDurable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced failed-COMMIT cover not cleaned: %v", err)
	}
	callerAfter, err := json.Marshal(next)
	if err != nil || !bytes.Equal(callerBefore, callerAfter) {
		t.Fatalf("caller changed during failed COMMIT: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	// Once external contention ends, this DB must start and commit a fresh
	// transaction instead of retaining an unfinished native transaction.
	if err := db.SaveProject(p); err != nil {
		t.Fatalf("connection poisoned after failed COMMIT: %v", err)
	}
	if got := mustGetProject(t, db, p.ID); !reflect.DeepEqual(got, previous) {
		t.Fatal("fresh save after failed COMMIT did not restore expected snapshot")
	}
	task, err := db.InsertTask("after commit failure", "output")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AppendLog(task, "connection recovered"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(task, true, ""); err != nil {
		t.Fatal(err)
	}
	history, err := db.GetHistory(1)
	if err != nil || len(history) != 1 || history[0].ID != task || !history[0].Success {
		t.Fatalf("history broken after failed COMMIT: %#v, %v", history, err)
	}
	logs, err := db.GetTaskLogs(task)
	if err != nil || len(logs) != 1 || logs[0].Message != "connection recovered" {
		t.Fatalf("logs broken after failed COMMIT: %#v, %v", logs, err)
	}
}
