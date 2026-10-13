package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/app"
	"chaptersmith/internal/store"
)

func TestAnalysisShutdownOrder(t *testing.T) {
	for _, first := range []string{"book", "merger"} {
		t.Run(first, func(t *testing.T) {
			root := t.TempDir()
			db, err := store.Open(filepath.Join(root, "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			b, m, err := NewAnalysisServices(db, filepath.Join(root, "cache"), filepath.Join(root, "projects"))
			if err != nil {
				t.Fatal(err)
			}
			defer b.ServiceShutdown()
			_, _, p := newBookFixture(t)
			b.mu.Lock()
			b.project = p
			b.revision = 1
			b.mu.Unlock()
			b.deps.Emit = func(string, any) {}
			m.deps.Emit = func(string, any) {}
			bstarted, mstarted, bcan, mcan, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			b.deps.Analyze = func(ctx context.Context, _ analysis.Input, o analysis.Options, _ func(analysis.Progress)) (analysis.Report, error) {
				if o.CacheDir != b.analysisOwner.Dir() {
					t.Error("unowned cache")
				}
				close(bstarted)
				<-ctx.Done()
				close(bcan)
				<-release
				if _, err := os.Stat(o.CacheDir); err != nil {
					t.Error("owner closed before join")
				}
				return analysis.Report{}, ctx.Err()
			}
			m.deps.Run = func(ctx context.Context, _ app.Config) error {
				close(mstarted)
				<-ctx.Done()
				close(mcan)
				<-release
				if _, err := db.GetHistory(1); err != nil {
					t.Error("DB closed early")
				}
				return ctx.Err()
			}
			go b.AnalyzeAudio("work", 1)
			<-bstarted
			m.StartMerge("input", "output")
			<-mstarted
			done := make(chan error, 2)
			if first == "book" {
				go func() { done <- b.ServiceShutdown() }()
			} else {
				go func() { done <- m.ServiceShutdown() }()
			}
			for _, ch := range []chan struct{}{bcan, mcan} {
				select {
				case <-ch:
				case <-time.After(time.Second):
					t.Fatal("both must cancel before joining")
				}
			}
			if _, err := b.AnalyzeAudio("late", 1); err == nil {
				t.Fatal("book reopened")
			}
			if err := m.StartMerge("late", "out"); err == nil {
				t.Fatal("merger reopened")
			}
			b.Media.mu.Lock()
			closed := b.Media.closed
			b.Media.mu.Unlock()
			if closed {
				t.Fatal("registry closed before jobs joined")
			}
			ownerDir := b.analysisOwner.Dir()
			go func() { done <- b.ServiceShutdown() }()
			close(release)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			if err := m.ServiceShutdown(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(ownerDir); !os.IsNotExist(err) {
				t.Fatal("owner retained")
			}
			if _, err := db.GetHistory(1); err != nil {
				t.Fatal("coordinator owns caller DB")
			}
		})
	}
}

func TestAnalysisShutdownStandaloneBorrowedMedia(t *testing.T) {
	b, db, _ := newBookFixture(t)
	m := NewMergerServiceWithAnalysis(db, b.Media, b.cacheDir)
	if err := m.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	path := renderedPreview(t, filepath.Join(b.cacheDir, "preview"))
	ref, err := b.Media.Register(path, "audio/mp4")
	if err != nil {
		t.Fatal("standalone merger closed borrowed registry", err)
	}
	if err = b.Media.Release(ref.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAnalysisShutdownOnStartupError(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	run := func() error {
		defer db.Close()
		b, m, err := NewAnalysisServices(db, filepath.Join(root, "cache"), filepath.Join(root, "projects"))
		if err != nil {
			return err
		}
		defer b.ServiceShutdown()
		owner = b.analysisOwner.Dir()
		started := make(chan struct{})
		m.deps.Emit = func(string, any) {}
		m.deps.Run = func(ctx context.Context, _ app.Config) error {
			close(started)
			<-ctx.Done()
			if _, err := db.GetHistory(1); err != nil {
				t.Error("DB closed before deferred service join")
			}
			return ctx.Err()
		}
		if err := m.StartMerge("input", "output"); err != nil {
			return err
		}
		<-started
		return context.Canceled // Simulates startup/Run returning an error after services exist.
	}
	if err = run(); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err = os.Stat(owner); !os.IsNotExist(err) {
		t.Fatal("error exit leaked owner")
	}
}
