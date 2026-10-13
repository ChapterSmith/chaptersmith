package book

import (
	"reflect"
	"testing"
)

func TestSetChapterPausesReplacesExistingSilenceWithOneLength(t *testing.T) {
	original := projectFixture(t)
	first, second := original.Sources[0].ID, original.Sources[1].ID
	// 1.5 s of silence ends the first chapter and 0.5 s begins the second.
	existing := []ChapterPause{{ChapterID: original.Chapters[1].ID, BeforeUS: 1_500_000, AfterUS: 500_000}}
	got, err := SetChapterPauses(original, existing, 2_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Segment{{SourceID: first, EndUS: 8_500_000}, {SilenceUS: 2_000_000}}; !reflect.DeepEqual(got.Chapters[0].Segments, want) {
		t.Fatalf("first chapter %+v, want %+v", got.Chapters[0].Segments, want)
	}
	if want := []Segment{{SourceID: second, StartUS: 500_000, EndUS: 10_000_000}}; !reflect.DeepEqual(got.Chapters[1].Segments, want) {
		t.Fatalf("second chapter %+v, want %+v", got.Chapters[1].Segments, want)
	}
	if DurationUS(original) != 20_000_000 {
		t.Fatal("the original snapshot changed")
	}
}

func TestSetChapterPausesOnlyAddsWhereNothingWasMeasuredAndCanCloseGaps(t *testing.T) {
	original := projectFixture(t)
	added, err := SetChapterPauses(original, nil, 3_000_000)
	if err != nil || DurationUS(added) != 23_000_000 || added.Chapters[0].Segments[1].SilenceUS != 3_000_000 || len(added.Chapters[1].Segments) != 1 {
		t.Fatalf("adding a pause: err=%v chapters=%+v", err, added.Chapters)
	}
	closed, err := SetChapterPauses(original, []ChapterPause{{ChapterID: original.Chapters[1].ID, BeforeUS: 1_000_000}}, 0)
	if err != nil || DurationUS(closed) != 19_000_000 || len(closed.Chapters[0].Segments) != 1 {
		t.Fatalf("closing a gap: err=%v chapters=%+v", err, closed.Chapters)
	}
}

func TestSetChapterPausesNeverEmptiesAChapterAndRejectsBadInput(t *testing.T) {
	original := projectFixture(t)
	second := original.Chapters[1].ID
	// The whole first chapter is reported as silence; it must keep its audio.
	kept, err := SetChapterPauses(original, []ChapterPause{{ChapterID: second, BeforeUS: 10_000_000}}, 1_000_000)
	if err != nil || kept.Chapters[0].Segments[0].EndUS != 10_000_000 {
		t.Fatalf("an all-silent chapter: err=%v segments=%+v", err, kept.Chapters[0].Segments)
	}
	for _, bad := range [][]ChapterPause{{{ChapterID: "missing"}}, {{ChapterID: original.Chapters[0].ID}}, {{ChapterID: second, BeforeUS: -1}}} {
		if _, err := SetChapterPauses(original, bad, 1_000_000); err == nil {
			t.Errorf("pauses %+v were accepted", bad)
		}
	}
	if _, err := SetChapterPauses(original, nil, -1); err == nil {
		t.Error("a negative pause was accepted")
	}
	single := Clone(original)
	single.Chapters = single.Chapters[:1]
	if _, err := SetChapterPauses(single, nil, 1_000_000); err == nil {
		t.Error("a single-chapter book has no pause to set")
	}
}
