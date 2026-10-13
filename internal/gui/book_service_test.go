package gui

import (
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"
	"chaptersmith/internal/store"
)

func newBookFixture(t *testing.T) (*BookService, *store.DB, book.BookProject) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewBookService(db, filepath.Join(root, "cache"), filepath.Join(root, "projects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ServiceShutdown(); _ = db.Close() })
	source := filepath.Join(root, "source.wav")
	if err := os.WriteFile(source, []byte("untouched audio source"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(root, "cover.png")
	f, err := os.Create(cover)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	p := book.BookProject{ID: "8b9fb7c8-b744-47d1-bfab-b02a797c20c3", SchemaVersion: 1,
		Sources:  []book.Source{{ID: "s", Path: source, DurationUS: 20000000, SizeBytes: st.Size(), MTimeNS: st.ModTime().UnixNano(), SampleRate: 44100, Channels: 2}},
		Chapters: []book.Chapter{{ID: "c1", Title: "First", Segments: []book.Segment{{SourceID: "s", EndUS: 10000000}}}, {ID: "c2", Title: "Second", Segments: []book.Segment{{SourceID: "s", StartUS: 10000000, EndUS: 20000000}}}},
		Metadata: meta.AudiobookMetadata{Title: "Book", Description: "Description", Extra: map[string]string{"language": "pl"}}, CoverPath: cover,
		Export: book.ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"}}
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) { return book.Clone(p), nil }
	s.deps.Emit = func(string, any) {}
	return s, db, p
}

func loadedBook(t *testing.T, s *BookService) ProjectView {
	t.Helper()
	v, err := s.LoadBook("initial", LoadRequest{InputPath: "selected"})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Catches replacing intentional empty metadata with imported values and losing durable covers.
func TestBookSaveReopenBlanks(t *testing.T) {
	s, db, p := newBookFixture(t)
	v := loadedBook(t, s)
	saved, err := s.SaveProject(v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := db.GetProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Project.CoverPath != durable.CoverPath || saved.Project.CoverPath == p.CoverPath {
		t.Fatal("session did not adopt durable cover")
	}
	if saved.Revision != v.Revision || saved.CanUndo || saved.Dirty {
		t.Fatalf("save changed session history: %+v", saved)
	}
	if err := os.Remove(p.CoverPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved.Project.CoverPath); err != nil {
		t.Fatal(err)
	}
	metadata := saved.Project.Metadata
	metadata.Title = ""
	metadata.Description = ""
	v, err = s.ApplyEdit(saved.Revision, EditRequest{Kind: "metadata", Metadata: &metadata})
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"})
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.SaveProject(v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := s.OpenProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Project.Metadata.Title != "" || reopened.Project.Metadata.Description != "" || reopened.Project.CoverPath != "" || reopened.Project.Metadata.Extra["language"] != "pl" || reopened.Dirty || reopened.CanUndo {
		t.Fatalf("literal blanks or Extra lost: %+v", reopened)
	}
	if reopened.DurationUS != 20000000 || len(reopened.ChapterRows) != 2 || reopened.ChapterRows[1].StartUS != 10000000 || reopened.ChapterRows[1].DurationUS != 10000000 || reopened.CoverURL != "" {
		t.Fatalf("bad derived view: %+v", reopened)
	}
	data, err := os.ReadFile(p.Sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p.Sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "untouched audio source" || st.ModTime().UnixNano() != p.Sources[0].MTimeNS {
		t.Fatal("source changed")
	}
	summaries, err := s.ListProjects(10)
	if err != nil || len(summaries) != 1 || summaries[0].Title != "" {
		t.Fatalf("bad summaries: %+v, %v", summaries, err)
	}
}

// Catches stale writes, partial commits, and aliases across edits, views, and undo.
func TestBookRevisionAndUndo(t *testing.T) {
	s, _, _ := newBookFixture(t)
	prior := loadedBook(t, s)
	if _, err := s.ApplyEdit(prior.Revision-1, EditRequest{Kind: "cover-remove"}); err == nil || !strings.Contains(err.Error(), "stale revision") {
		t.Fatalf("stale edit accepted: %v", err)
	}
	for _, edit := range []EditRequest{{Kind: "metadata"}, {Kind: "unknown"}} {
		if _, err := s.ApplyEdit(prior.Revision, edit); err == nil {
			t.Fatal("invalid edit accepted")
		}
	}
	current, err := s.GetProject()
	if err != nil || !reflect.DeepEqual(current, prior) {
		t.Fatal("failed edit changed view")
	}
	metadata := prior.Project.Metadata
	metadata.Extra = map[string]string{"language": "en"}
	metadata.Title = "Changed"
	edited, err := s.ApplyEdit(prior.Revision, EditRequest{Kind: "metadata", Metadata: &metadata})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != prior.Revision+1 || !edited.Dirty || !edited.CanUndo {
		t.Fatal("bad edit revision")
	}
	metadata.Extra["language"] = "caller mutation"
	edited.Project.Metadata.Extra["language"] = "view mutation"
	edited.Project.Chapters[0].Segments[0].EndUS = 1
	undone, err := s.Undo(edited.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(undone.Project, prior.Project) || undone.Revision != edited.Revision+1 || !undone.Dirty || undone.CanUndo {
		t.Fatalf("undo failed: %+v", undone)
	}
	undone.Project.Metadata.Extra["language"] = "returned mutation"
	undone.Project.Sources[0].Path = "mutation"
	current, err = s.GetProject()
	if err != nil || !reflect.DeepEqual(current.Project, prior.Project) {
		t.Fatal("view alias changed session")
	}
	if _, err := s.Undo(current.Revision); err == nil {
		t.Fatal("empty undo accepted")
	}
	for i := 0; i < 51; i++ {
		current, err = s.ApplyEdit(current.Revision, EditRequest{Kind: "cover-remove"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		current, err = s.Undo(current.Revision)
		if err != nil {
			t.Fatal(err)
		}
	}
	if current.CanUndo {
		t.Fatal("undo exceeded 50 snapshots")
	}
}

// Catches source fingerprint refresh or rejecting inspectable saved projects.
func TestBookOpenMissingSources(t *testing.T) {
	s, db, p := newBookFixture(t)
	if err := db.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.Sources[0].Path); err != nil {
		t.Fatal(err)
	}
	v, err := s.OpenProject(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.SourceWarning == "" || !reflect.DeepEqual(v.Project.Sources, p.Sources) || v.Dirty {
		t.Fatalf("missing source not preserved: %+v", v)
	}
	metadata := v.Project.Metadata
	metadata.Title = "Still editable"
	if _, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "metadata", Metadata: &metadata}); err != nil {
		t.Fatal(err)
	}
}

// Catches holding the session lock during dialogs or accepting a stale cover choice.
func TestBookCoverPickerUnlockedAndRevisionChecked(t *testing.T) {
	s, _, p := newBookFixture(t)
	v := loadedBook(t, s)
	s.deps.Pick = func(kind string) (string, error) {
		if kind != "cover" {
			t.Fatalf("unexpected picker: %s", kind)
		}
		if _, err := s.GetProject(); err != nil {
			t.Fatal(err)
		}
		return "", nil
	}
	cancelled, err := s.SelectCover(v.Revision)
	if err != nil || !reflect.DeepEqual(cancelled, v) {
		t.Fatalf("cancel changed view: %+v %v", cancelled, err)
	}
	s.deps.Pick = func(string) (string, error) {
		_, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"})
		return p.CoverPath, err
	}
	if _, err := s.SelectCover(v.Revision); err == nil || !strings.Contains(err.Error(), "stale revision") {
		t.Fatalf("stale picker committed: %v", err)
	}
	current, err := s.GetProject()
	if err != nil {
		t.Fatal(err)
	}
	s.deps.Pick = func(string) (string, error) { return p.CoverPath, nil }
	cover, err := s.SelectCover(current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCovers, err := filepath.EvalSymlinks(filepath.Join(s.cacheDir, "covers"))
	if err != nil {
		t.Fatal(err)
	}
	if cover.Project.CoverPath == p.CoverPath || !strings.HasPrefix(cover.Project.CoverPath, canonicalCovers+string(os.PathSeparator)) || cover.Revision != current.Revision+1 {
		t.Fatalf("cover not cached: %+v", cover)
	}
}

// Catches two concurrent commands accepting the same revision or exposing a partial edit.
func TestBookConcurrentEditsCommitOnce(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; _, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"}); results <- err }()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "stale revision") {
			t.Fatal(err)
		}
	}
	current, err := s.GetProject()
	if err != nil {
		t.Fatal(err)
	}
	if successes != 1 || current.Revision != v.Revision+1 || current.Project.CoverPath != "" {
		t.Fatalf("concurrent edit was not atomic: %d %+v", successes, current)
	}
}

// Catches failed saves falsely marking edits clean and clearing an explicitly empty Extra snapshot.
func TestBookFailedSavePreservesDirtyAndEmptyExtra(t *testing.T) {
	s, db, _ := newBookFixture(t)
	v := loadedBook(t, s)
	metadata := v.Project.Metadata
	metadata.Extra = map[string]string{}
	edited, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "metadata", Metadata: &metadata})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveProject(edited.Revision)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.OpenProject(saved.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Project.Metadata.Extra == nil || len(opened.Project.Metadata.Extra) != 0 {
		t.Fatal("empty Extra not preserved")
	}
	dirty, err := s.ApplyEdit(opened.Revision, EditRequest{Kind: "cover-remove"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProject(dirty.Revision); err == nil {
		t.Fatal("closed database save accepted")
	}
	after, err := s.GetProject()
	if err != nil || !reflect.DeepEqual(after, dirty) || !after.Dirty {
		t.Fatalf("failed save changed session: %+v %v", after, err)
	}
}
