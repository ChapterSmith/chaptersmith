package gui

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"chaptersmith/internal/book"
)

// Catches lost explicit import selections and unintended state changes on ambiguity/cancel.
func TestBookLoadCancelAndAmbiguity(t *testing.T) {
	s, _, p := newBookFixture(t)
	prior := loadedBook(t, s)
	s.deps.Pick = func(kind string) (string, error) {
		if _, err := s.GetProject(); err != nil {
			t.Fatal(err)
		}
		return "", nil
	}
	for _, kind := range []string{"folder", "file"} {
		path, err := s.SelectBookInput(kind)
		if err != nil || path != "" {
			t.Fatalf("cancelled picker: %q %v", path, err)
		}
	}
	for _, kind := range []string{"playlist", "cue"} {
		path, err := s.SelectSidecar(kind)
		if err != nil || path != "" {
			t.Fatalf("cancelled sidecar: %q %v", path, err)
		}
	}
	if _, err := s.SelectBookInput("other"); err == nil {
		t.Fatal("invalid picker kind accepted")
	}
	if _, err := s.SelectSidecar("other"); err == nil {
		t.Fatal("invalid sidecar kind accepted")
	}
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) {
		return book.BookProject{}, errors.New("ambiguous chapters; choose source")
	}
	if _, err := s.LoadBook("ambiguous", LoadRequest{InputPath: "folder"}); err == nil {
		t.Fatal("ambiguity ignored")
	}
	current, err := s.GetProject()
	if err != nil || !reflect.DeepEqual(current, prior) {
		t.Fatal("ambiguity replaced project")
	}
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) {
		return book.BookProject{}, context.Canceled
	}
	if _, err := s.LoadBook("cancelled", LoadRequest{InputPath: "folder"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel not reported: %v", err)
	}
	current, err = s.GetProject()
	if err != nil || !reflect.DeepEqual(current, prior) {
		t.Fatal("cancel replaced project")
	}
	var got book.ImportOptions
	s.deps.Import = func(_ context.Context, opts book.ImportOptions) (book.BookProject, error) { got = opts; return p, nil }
	v, err := s.LoadBook("retry", LoadRequest{InputPath: "folder", PlaylistPath: "order.m3u", CuePath: "chapters.cue", ChapterSource: "cue"})
	if err != nil {
		t.Fatal(err)
	}
	if got.InputPath != "folder" || got.PlaylistPath != "order.m3u" || got.CuePath != "chapters.cue" || got.ChapterSource != "cue" || got.CacheDir != filepath.Join(s.cacheDir, "covers") || v.Revision != prior.Revision+1 || !v.Dirty {
		t.Fatalf("bad retry: %+v %+v", got, v)
	}
}

// Catches early slot release, double terminal events, and shutdown returning before cleanup.
func TestBookOperationLifecycle(t *testing.T) {
	s, _, p := newBookFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var eventMu sync.Mutex
	var events []OperationStatus
	s.deps.Emit = func(name string, payload any) {
		if name == "book-operation" {
			eventMu.Lock()
			events = append(events, payload.(OperationStatus))
			eventMu.Unlock()
		}
	}
	s.deps.Import = func(ctx context.Context, _ book.ImportOptions) (book.BookProject, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return p, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.LoadBook("running", LoadRequest{InputPath: "input"}); done <- err }()
	<-entered
	if _, err := s.LoadBook("busy", LoadRequest{}); err == nil || !strings.Contains(err.Error(), "operation already running") {
		t.Fatalf("busy not rejected: %v", err)
	}
	if err := s.CancelOperation("running"); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelOperation("running"); err != nil {
		t.Fatal(err)
	}
	status, err := s.GetOperation("running")
	if err != nil || status.Status != "cancelling" || status.Kind != "load" || status.Revision != 0 || status.RequestID != "running" {
		t.Fatalf("bad cancellation: %+v %v", status, err)
	}
	if _, err := s.LoadBook("still-busy", LoadRequest{}); err == nil || !strings.Contains(err.Error(), "operation already running") {
		t.Fatalf("cancel released slot early: %v", err)
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled import committed: %v", err)
	}
	status, err = s.GetOperation("running")
	if err != nil || status.Status != "cancelled" || status.Error == "" || len(status.Paths) != 0 {
		t.Fatalf("terminal status missing: %+v %v", status, err)
	}
	eventMu.Lock()
	terminals := 0
	for _, e := range events {
		if e.RequestID == "running" && (e.Status == "succeeded" || e.Status == "failed" || e.Status == "cancelled") {
			terminals++
		}
	}
	eventMu.Unlock()
	if terminals != 1 {
		t.Fatalf("terminal event count %d", terminals)
	}
	if err := s.CancelOperation("running"); err != nil {
		t.Fatal(err)
	}
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) { return p, nil }
	if _, err := s.LoadBook("next", LoadRequest{InputPath: "input"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadBook("next", LoadRequest{}); err == nil {
		t.Fatal("reused request id accepted")
	}
	if _, err := s.GetOperation("missing"); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if err := s.CancelOperation("missing"); err == nil {
		t.Fatal("unknown cancel accepted")
	}
	entered, release = make(chan struct{}), make(chan struct{})
	s.deps.Import = func(ctx context.Context, _ book.ImportOptions) (book.BookProject, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return book.BookProject{}, ctx.Err()
	}
	go func() { _, err := s.LoadBook("shutdown", LoadRequest{InputPath: "input"}); done <- err }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- s.ServiceShutdown() }()
	deadline := time.After(2 * time.Second)
	for {
		st, err := s.GetOperation("shutdown")
		if err != nil {
			t.Fatal(err)
		}
		if st.Status == "cancelling" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("shutdown did not cancel")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case <-closed:
		t.Fatal("shutdown skipped join")
	default:
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.LoadBook("after-shutdown", LoadRequest{}); err == nil {
		t.Fatal("closed service accepted load")
	}
}

// Catches mutation/project switches during load and session mutexes held inside event callbacks.
func TestBookLoadBlocksMutationAndUnlocksEvents(t *testing.T) {
	s, _, p := newBookFixture(t)
	prior := loadedBook(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s.deps.Import = func(context.Context, book.ImportOptions) (book.BookProject, error) {
		close(entered)
		<-release
		return p, nil
	}
	s.deps.Emit = func(_ string, payload any) {
		status := payload.(OperationStatus)
		if _, err := s.GetOperation(status.RequestID); err != nil {
			t.Error(err)
		}
		if _, err := s.GetProject(); err != nil {
			t.Error(err)
		}
	}
	done := make(chan error, 1)
	go func() { _, err := s.LoadBook("blocked", LoadRequest{InputPath: "input"}); done <- err }()
	<-entered
	for _, call := range []func() error{
		func() error { _, err := s.ApplyEdit(prior.Revision, EditRequest{Kind: "cover-remove"}); return err },
		func() error { _, err := s.SaveProject(prior.Revision); return err },
		func() error { _, err := s.Undo(prior.Revision); return err },
		func() error { _, err := s.OpenProject(p.ID); return err },
		func() error { _, err := s.SelectCover(prior.Revision); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "operation already running") {
			t.Fatalf("mutation during load: %v", err)
		}
	}
	current, err := s.GetProject()
	if err != nil || !reflect.DeepEqual(current, prior) {
		t.Fatal("blocked operation changed session")
	}
	s.updateOperation("blocked", 1, 2, "Reading")
	status, err := s.GetOperation("blocked")
	if err != nil || status.Current != 1 || status.Total != 2 || status.Message != "Reading" {
		t.Fatalf("progress missing: %+v %v", status, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Catches cancellation being misreported as validation failure for a late importer result.
func TestBookCancelledInvalidImportKeepsProject(t *testing.T) {
	s, _, _ := newBookFixture(t)
	prior := loadedBook(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s.deps.Import = func(ctx context.Context, _ book.ImportOptions) (book.BookProject, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return book.BookProject{}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.LoadBook("invalid-cancel", LoadRequest{InputPath: "input"}); done <- err }()
	<-entered
	if err := s.CancelOperation("invalid-cancel"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled invalid import returned %v", err)
	}
	current, err := s.GetProject()
	if err != nil || !reflect.DeepEqual(current, prior) {
		t.Fatal("cancelled import changed session")
	}
	status, err := s.GetOperation("invalid-cancel")
	if err != nil || status.Status != "cancelled" {
		t.Fatalf("cancelled import status: %+v %v", status, err)
	}
}
