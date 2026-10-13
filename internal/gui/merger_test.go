package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"chaptersmith/internal/store"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestStartMergeReleasesStateAfterDatabaseFailure(t *testing.T) {
	application.New(application.Options{Name: "Merger test"})
	db, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := NewMergerService(db)
	if err := s.StartMerge(t.TempDir(), "out.m4b"); err == nil {
		t.Fatal("expected closed database error")
	}
	if err := s.StartMerge(t.TempDir(), "out.m4b"); err == nil {
		t.Fatal("failed start left service permanently busy and falsely returned success")
	}
}

func TestStartMergeRejectsConcurrentTask(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &MergerService{cfg: &mergeConfig{cancel: cancel}}
	if err := s.StartMerge("input", "out.m4b"); err == nil {
		t.Fatal("concurrent merge silently accepted")
	}
}

func TestCancelMergeWhileWaitingForFixChoice(t *testing.T) {
	a := application.New(application.Options{Name: "Merger test"})
	db, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	data, err := os.ReadFile("../../testdata/formats/m4a/01.m4a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.m4a"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := NewMergerService(db)
	done := make(chan struct{}, 1)
	stopPrompt := a.Event.On("task-prompt-fix", func(*application.CustomEvent) { s.CancelMerge() })
	defer stopPrompt()
	stopDone := a.Event.On("task-done", func(*application.CustomEvent) { done <- struct{}{} })
	defer stopDone()
	if err := s.StartMerge(dir, filepath.Join(dir, "out.m4b")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		s.RespondFixPrompt("stop")
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("cancel cannot release task waiting for fix prompt")
	}
}
