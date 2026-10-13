package gui

import (
	"context"
	"testing"

	"chaptersmith/internal/book"
)

func TestSuggestSplitsReportsProposalsWithoutChangingTheBook(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	want := []book.SplitProposal{{AtUS: 4000000, SilenceUS: 2000000}}
	var asked book.SplitOptions
	s.deps.Splits = func(_ context.Context, _ book.BookProject, opts book.SplitOptions, progress func(int64, int64)) ([]book.SplitProposal, error) {
		asked = opts
		progress(10000000, 20000000)
		return want, nil
	}
	opts := book.SplitOptions{MinSilenceUS: 2000000, ThresholdDB: -40, MinChapterUS: 300000000}
	got, err := s.SuggestSplits("splits", v.Revision, opts)
	if err != nil {
		t.Fatal(err)
	}
	if asked != opts || len(got.Proposals) != 1 || got.Proposals[0] != want[0] || got.Revision != v.Revision || got.RequestID != "splits" {
		t.Fatalf("asked %+v, got %+v", asked, got)
	}
	after, _ := s.GetProject()
	if after.Revision != v.Revision || len(after.ChapterRows) != len(v.ChapterRows) {
		t.Fatal("suggesting splits changed the book")
	}
}

func TestSplitAndMergeEditsAreSingleUndoableSteps(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	chapters := len(v.ChapterRows)
	split, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "split", PositionsUS: []int64{4000000, 15000000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(split.ChapterRows) != chapters+2 || split.DurationUS != v.DurationUS {
		t.Fatalf("after split: %d chapters, %d µs", len(split.ChapterRows), split.DurationUS)
	}
	merged, err := s.ApplyEdit(split.Revision, EditRequest{Kind: "merge", ChapterID: split.ChapterRows[1].ID})
	if err != nil || len(merged.ChapterRows) != chapters+1 {
		t.Fatalf("after merge: err=%v, %d chapters", err, len(merged.ChapterRows))
	}
	undone, err := s.Undo(merged.Revision)
	if err != nil || len(undone.ChapterRows) != chapters+2 {
		t.Fatalf("undo of merge: err=%v, %d chapters", err, len(undone.ChapterRows))
	}
	undone, err = s.Undo(undone.Revision)
	if err != nil || len(undone.ChapterRows) != chapters {
		t.Fatalf("undo of split: err=%v, %d chapters", err, len(undone.ChapterRows))
	}
	if _, err = s.ApplyEdit(undone.Revision, EditRequest{Kind: "split", PositionsUS: []int64{0}}); err == nil {
		t.Fatal("a split at a chapter boundary was accepted")
	}
}

func TestPreviewPositionRejectsPlacesOutsideTheBook(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	for _, at := range []int64{0, -5, v.DurationUS, v.DurationUS + 1} {
		if _, err := s.PreviewPosition("p", v.Revision, at); err == nil {
			t.Errorf("position %d was previewed", at)
		}
	}
}

func TestPreviewWindowSurroundsThePositionAndStaysInsideTheBook(t *testing.T) {
	s, _, p := newBookFixture(t)
	loadedBook(t, s)
	window, origin, err := previewWindow(p, 12000000, 12000000)
	if err != nil || origin != 7000000 || book.DurationUS(window) != 10000000 {
		t.Fatalf("mid-book window: err=%v origin=%d length=%d", err, origin, book.DurationUS(window))
	}
	window, origin, err = previewWindow(p, 2000000, 2000000)
	if err != nil || origin != 0 || book.DurationUS(window) != 7000000 {
		t.Fatalf("window near the start: err=%v origin=%d length=%d", err, origin, book.DurationUS(window))
	}
}

func TestMeasuredPausesAreReplacedByOneEdit(t *testing.T) {
	s, _, p := newBookFixture(t)
	v := loadedBook(t, s)
	second := v.ChapterRows[1].ID
	s.deps.Pauses = func(_ context.Context, got book.BookProject, threshold float64, window int64) ([]book.ChapterPause, error) {
		if got.ID != p.ID || threshold != -40 || window != 5000000 {
			t.Errorf("measured project %q with threshold %v and window %d", got.ID, threshold, window)
		}
		return []book.ChapterPause{{ChapterID: second, BeforeUS: 1000000, AfterUS: 500000}}, nil
	}
	measured, err := s.MeasureChapterPauses("pauses", v.Revision, PauseOptions{ThresholdDB: -40, WindowUS: 5000000})
	if err != nil || len(measured.Pauses) != 1 || measured.Revision != v.Revision {
		t.Fatalf("err=%v measured=%+v", err, measured)
	}
	set, err := s.ApplyEdit(v.Revision, EditRequest{Kind: "pauses", Pauses: measured.Pauses, DurationUS: 2000000})
	if err != nil {
		t.Fatal(err)
	}
	// 1.5 s of existing silence leaves, 2 s of chosen pause arrives.
	if set.DurationUS != v.DurationUS+500000 {
		t.Fatalf("book is %d µs, want %d", set.DurationUS, v.DurationUS+500000)
	}
	undone, err := s.Undo(set.Revision)
	if err != nil || undone.DurationUS != v.DurationUS {
		t.Fatalf("undo: err=%v length=%d", err, undone.DurationUS)
	}
}
