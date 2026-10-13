package gui

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chaptersmith/internal/book"
	"chaptersmith/internal/render"
)

func TestBookExportSnapshotAndBusy(t *testing.T) {
	s, db, p := newBookFixture(t)
	v := loadedBook(t, s)
	entered, release := make(chan book.BookProject, 1), make(chan struct{})
	s.deps.Export = func(ctx context.Context, p book.BookProject, path string, l *log.Logger, progress func(int, int, string)) (render.ExportResult, error) {
		entered <- p
		<-release
		l.Print("snapshot rendered")
		return render.ExportResult{Paths: []string{path}, NormalizationReport: "Preserve fallback: edited timeline"}, nil
	}
	started, err := s.StartExport("export", v.Revision, filepath.Join(t.TempDir(), "book.m4b"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := <-entered
	metadata := p.Metadata
	metadata.Title = "changed"
	v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "metadata", Metadata: &metadata})
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"})
	if err != nil {
		t.Fatal(err)
	}
	settings := p.Export
	settings.Format = "wav"
	v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "export", Export: &settings})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveProject(v.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartExport("duplicate", v.Revision, "ignored.wav"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err = s.OpenProject(p.ID); err == nil {
		t.Fatal("switch accepted")
	}
	rows, err := db.GetHistory(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("tasks: %+v %v", rows, err)
	}
	var stored book.BookProject
	if err = json.Unmarshal([]byte(*rows[0].ProjectSnapshot), &stored); err != nil {
		t.Fatal(err)
	}
	if snapshot.Metadata.Title != "Book" || stored.Metadata.Title != "Book" || snapshot.CoverPath == "" || stored.Export.Format != "m4b" {
		t.Fatal("mutable snapshot")
	}
	close(release)
	s.workers.Wait()
	status, err := s.GetOperation(started.RequestID)
	if err != nil || status.Status != "succeeded" || len(status.Paths) != 1 || status.NormalizationReport == "" || status.Revision != started.Revision {
		t.Fatalf("status %+v %v", status, err)
	}
	logs, err := db.GetTaskLogs(started.TaskID)
	if err != nil || len(logs) == 0 {
		t.Fatalf("logs %+v %v", logs, err)
	}
}
func TestBookExportEarlyDone(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	s.deps.Export = func(context.Context, book.BookProject, string, *log.Logger, func(int, int, string)) (render.ExportResult, error) {
		return render.ExportResult{Paths: []string{"published"}}, nil
	}
	started, err := s.StartExport("early", v.Revision, filepath.Join(t.TempDir(), "book.m4b"))
	if err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	status, _ := s.GetOperation(started.RequestID)
	if status.Status != "succeeded" {
		t.Fatal(status)
	}
}
func TestBookExportCancelCleanup(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup", true: "published"}[published], func(t *testing.T) {
			s, db, _ := newBookFixture(t)
			v := loadedBook(t, s)
			entered, cleanup, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			s.deps.Export = func(ctx context.Context, p book.BookProject, path string, l *log.Logger, progress func(int, int, string)) (render.ExportResult, error) {
				close(entered)
				<-ctx.Done()
				close(cleanup)
				<-release
				if published {
					return render.ExportResult{Paths: []string{path}}, nil
				}
				return render.ExportResult{}, ctx.Err()
			}
			started, err := s.StartExport("cancel", v.Revision, filepath.Join(t.TempDir(), "book.m4b"))
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			if err = s.CancelOperation("cancel"); err != nil {
				t.Fatal(err)
			}
			<-cleanup
			if _, err = s.StartExport("busy", v.Revision, "x.m4b"); err == nil {
				t.Fatal("released before cleanup")
			}
			close(release)
			s.workers.Wait()
			status, _ := s.GetOperation("cancel")
			want := "cancelled"
			if published {
				want = "succeeded"
			}
			if status.Status != want {
				t.Fatal(status)
			}
			rows, _ := db.GetHistory(10)
			if rows[0].ID != started.TaskID || rows[0].Success != published {
				t.Fatal(rows)
			}
			if !published && (rows[0].ErrorMessage == nil || *rows[0].ErrorMessage != "cancelled") {
				t.Fatal(rows)
			}
		})
	}
}
func TestBookExportStoreFailure(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		s, db, _ := newBookFixture(t)
		v := loadedBook(t, s)
		db.Close()
		called := false
		s.deps.Export = func(context.Context, book.BookProject, string, *log.Logger, func(int, int, string)) (render.ExportResult, error) {
			called = true
			return render.ExportResult{}, nil
		}
		if _, err := s.StartExport("fail", v.Revision, filepath.Join(t.TempDir(), "book.m4b")); err == nil {
			t.Fatal("insert accepted")
		}
		if called || s.operation != nil {
			t.Fatal("leaked worker")
		}
	})
	t.Run("complete and log", func(t *testing.T) {
		s, db, _ := newBookFixture(t)
		v := loadedBook(t, s)
		s.deps.Export = func(ctx context.Context, p book.BookProject, path string, l *log.Logger, _ func(int, int, string)) (render.ExportResult, error) {
			db.Close()
			l.Print("publication complete")
			return render.ExportResult{Paths: []string{path}}, nil
		}
		_, err := s.StartExport("fail", v.Revision, filepath.Join(t.TempDir(), "book.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		s.workers.Wait()
		status, _ := s.GetOperation("fail")
		if len(status.Paths) != 1 || !strings.Contains(status.Error, "history") || status.Status != "succeeded" {
			t.Fatal(status)
		}
	})
}
func TestBookExportSourceAndTarget(t *testing.T) {
	s, _, p := newBookFixture(t)
	v := loadedBook(t, s)
	parent := t.TempDir()
	s.deps.Pick = func(string) (string, error) { return parent, nil }
	settings := p.Export
	settings.OutputMode = "both"
	v, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "export", Export: &settings})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "CON", "trailing."} {
		if _, err = s.SelectExportTarget(v.Revision, name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	target, err := s.SelectExportTarget(v.Revision, "new book")
	if err != nil || target != filepath.Join(parent, "new book") {
		t.Fatalf("%s %v", target, err)
	}
	if _, err = os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("picker created output")
	}
	settings.OutputMode = "book"
	settings.Format = "flac"
	v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "export", Export: &settings})
	if err != nil {
		t.Fatal(err)
	}
	s.deps.Pick = func(kind string) (string, error) {
		if kind != "export:flac" {
			t.Fatal(kind)
		}
		return filepath.Join(parent, "new.m4b"), nil
	}
	target, err = s.SelectExportTarget(v.Revision, "")
	if err != nil || filepath.Ext(target) != ".flac" {
		t.Fatalf("%s %v", target, err)
	}
	os.Remove(p.Sources[0].Path)
	if _, err = s.StartExport("missing", v.Revision, target); err == nil {
		t.Fatal("missing source accepted")
	}
	if s.operation != nil {
		t.Fatal("source failure leaked slot")
	}
}
