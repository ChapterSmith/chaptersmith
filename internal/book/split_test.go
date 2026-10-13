package book

import (
	"reflect"
	"testing"
)

func chapterTitles(p BookProject) []string {
	var titles []string
	for _, c := range p.Chapters {
		titles = append(titles, c.Title)
	}
	return titles
}

func TestSplitChaptersCutsAtOutputPositionsInOneEdit(t *testing.T) {
	original := projectFixture(t)
	first, second := original.Sources[0].ID, original.Sources[1].ID
	// Out of order and repeated: 15 s falls in the second chapter, 4 s in the first.
	split, err := SplitChapters(original, []int64{15_000_000, 4_000_000, 15_000_000})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]Segment{
		{{SourceID: first, EndUS: 4_000_000}},
		{{SourceID: first, StartUS: 4_000_000, EndUS: 10_000_000}},
		{{SourceID: second, EndUS: 5_000_000}},
		{{SourceID: second, StartUS: 5_000_000, EndUS: 10_000_000}},
	}
	if len(split.Chapters) != len(want) {
		t.Fatalf("got %d chapters, want %d", len(split.Chapters), len(want))
	}
	ids := map[string]bool{}
	for i, c := range split.Chapters {
		if !reflect.DeepEqual(c.Segments, want[i]) {
			t.Fatalf("chapter %d segments %+v, want %+v", i, c.Segments, want[i])
		}
		ids[c.ID] = true
	}
	if len(ids) != 4 || split.Chapters[0].ID != original.Chapters[0].ID || split.Chapters[2].ID != original.Chapters[1].ID {
		t.Fatal("existing chapters must keep their IDs and new ones need unique IDs")
	}
	// New parts are numbered by where they end up in the book.
	if got := chapterTitles(split); !reflect.DeepEqual(got, []string{original.Chapters[0].Title, "Chapter 2", original.Chapters[1].Title, "Chapter 4"}) {
		t.Fatalf("titles %q", got)
	}
	if DurationUS(split) != DurationUS(original) || len(original.Chapters) != 2 {
		t.Fatal("splitting must not change audio or the original snapshot")
	}
}

func TestSplitChaptersRejectsBoundariesAndPositionsOutsideTheBook(t *testing.T) {
	original := projectFixture(t)
	for _, at := range []int64{0, 10_000_000, 20_000_000, 25_000_000, -1} {
		if _, err := SplitChapters(original, []int64{5_000_000, at}); err == nil {
			t.Errorf("split at %d was accepted", at)
		}
	}
	if _, err := SplitChapters(original, nil); err == nil {
		t.Error("an empty split was accepted")
	}
}

func TestMergeChapterJoinsWithThePreviousOneAndUndoesASplit(t *testing.T) {
	original := projectFixture(t)
	split, err := SplitChapters(original, []int64{4_000_000})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergeChapter(split, split.Chapters[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(merged.Chapters, original.Chapters) {
		t.Fatalf("merging a split did not restore the chapters: %+v", merged.Chapters)
	}
	// Different sources stay separate segments of the surviving chapter.
	joined, err := MergeChapter(original, original.Chapters[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(joined.Chapters) != 1 || len(joined.Chapters[0].Segments) != 2 || joined.Chapters[0].Title != original.Chapters[0].Title || DurationUS(joined) != DurationUS(original) {
		t.Fatalf("joined chapters %+v", joined.Chapters)
	}
	if _, err = MergeChapter(original, original.Chapters[0].ID); err == nil {
		t.Fatal("the first chapter has nothing before it to merge into")
	}
}
