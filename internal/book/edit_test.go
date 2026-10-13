package book

import (
	"math"
	"reflect"
	"testing"
)

func TestRenameAndMove(t *testing.T) {
	original := projectFixture(t)
	before := Clone(original)
	bID := original.Chapters[1].ID
	renamed, err := RenameChapter(original, bID, "  Ending  ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Chapters[1].Title != "Ending" || renamed.Chapters[1].ID != bID {
		t.Fatal("rename lost title or ID")
	}
	moved, err := MoveChapter(renamed, bID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Chapters[0].ID != bID || !reflect.DeepEqual(moved.Chapters[0].Segments, original.Chapters[1].Segments) {
		t.Fatal("chapter content did not move")
	}
	start, err := ChapterStartUS(moved, original.Chapters[0].ID)
	if err != nil || start != 10_000_000 || DurationUS(moved) != 20_000_000 {
		t.Fatalf("move timeline: start=%d err=%v duration=%d", start, err, DurationUS(moved))
	}
	restored, err := MoveChapter(moved, bID, 1)
	if err != nil || !reflect.DeepEqual(restored, renamed) {
		t.Fatalf("move back: %v", err)
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("edit changed original")
	}
	// Success results must also own the nested data that an undo state retains.
	renamed.Metadata.Extra["language"] = "en"
	renamed.Chapters[0].Segments[0].EndUS = 1
	if !reflect.DeepEqual(original, before) {
		t.Fatal("success result aliases original")
	}
}

func TestTransferForwardAndBackward(t *testing.T) {
	original := projectFixture(t)
	before := Clone(original)
	bID, sourceID := original.Chapters[1].ID, original.Sources[1].ID
	forward, err := ShiftBoundary(original, bID, 13_000_000, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	wantA := []Segment{{SourceID: original.Sources[0].ID, EndUS: 10_000_000}, {SourceID: sourceID, EndUS: 3_000_000}}
	wantB := []Segment{{SourceID: sourceID, StartUS: 3_000_000, EndUS: 10_000_000}}
	if !reflect.DeepEqual(forward.Chapters[0].Segments, wantA) || !reflect.DeepEqual(forward.Chapters[1].Segments, wantB) || DurationUS(forward) != 20_000_000 {
		t.Fatalf("forward transfer: %+v", forward.Chapters)
	}
	restored, err := ShiftBoundary(forward, bID, 10_000_000, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Chapters[0].Segments, original.Chapters[0].Segments) {
		t.Fatal("backward transfer did not restore previous chapter")
	}
	// Adjacent segments of one source may remain split, but their intervals must exactly cover B once.
	segments := restored.Chapters[1].Segments
	if len(segments) == 1 {
		if !reflect.DeepEqual(segments, original.Chapters[1].Segments) {
			t.Fatal("restored interval changed")
		}
	} else if !reflect.DeepEqual(segments, []Segment{{SourceID: sourceID, EndUS: 3_000_000}, {SourceID: sourceID, StartUS: 3_000_000, EndUS: 10_000_000}}) {
		t.Fatalf("restored intervals duplicate or omit audio: %+v", segments)
	}
	start, err := ChapterStartUS(restored, bID)
	if err != nil || start != 10_000_000 || DurationUS(restored) != 20_000_000 {
		t.Fatal("backward transfer timeline changed")
	}
	unchanged, err := ShiftBoundary(original, bID, 10_000_000, "transfer")
	if err != nil || !reflect.DeepEqual(unchanged, original) {
		t.Fatal("unchanged boundary should clone")
	}
	unchanged.Chapters[0].Segments[0].EndUS = 1
	if !reflect.DeepEqual(original, before) {
		t.Fatal("transfer changed original")
	}
}

func TestRemovePrefix(t *testing.T) {
	original := projectFixture(t)
	result, err := ShiftBoundary(original, original.Chapters[1].ID, 13_000_000, "remove")
	if err != nil {
		t.Fatal(err)
	}
	start, err := ChapterStartUS(result, original.Chapters[1].ID)
	if err != nil || start != 10_000_000 || DurationUS(result) != 17_000_000 {
		t.Fatal("removal did not close gap")
	}
	if !reflect.DeepEqual(result.Chapters[1].Segments, []Segment{{SourceID: original.Sources[1].ID, StartUS: 3_000_000, EndUS: 10_000_000}}) {
		t.Fatal("wrong prefix removed")
	}
	if DurationUS(original) != 20_000_000 {
		t.Fatal("remove changed original")
	}
}

func TestSilenceAndSplit(t *testing.T) {
	original := projectFixture(t)
	bID := original.Chapters[1].ID
	silent, err := AddSilence(original, bID, 2_000_000, "start")
	if err != nil {
		t.Fatal(err)
	}
	transferred, err := ShiftBoundary(silent, bID, 11_000_000, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transferred.Chapters[0].Segments, []Segment{{SourceID: original.Sources[0].ID, EndUS: 10_000_000}, {SilenceUS: 1_000_000}}) {
		t.Fatal("previous silence split incorrectly")
	}
	if !reflect.DeepEqual(transferred.Chapters[1].Segments, []Segment{{SilenceUS: 1_000_000}, {SourceID: original.Sources[1].ID, EndUS: 10_000_000}}) {
		t.Fatal("current silence or audio changed")
	}
	if DurationUS(transferred) != 22_000_000 || DurationUS(original) != 20_000_000 {
		t.Fatal("silence duration incorrect")
	}
	ended, err := AddSilence(original, original.Chapters[0].ID, 2_000_000, "end")
	if err != nil {
		t.Fatal(err)
	}
	backward, err := ShiftBoundary(ended, bID, 11_000_000, "transfer")
	if err != nil || backward.Chapters[0].Segments[1].SilenceUS != 1_000_000 || backward.Chapters[1].Segments[0].SilenceUS != 1_000_000 {
		t.Fatalf("backward silence split: %v", err)
	}
	// Cross multiple segments and a source boundary in one operation.
	crossed, err := ShiftBoundary(ended, bID, 9_000_000, "transfer")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(crossed.Chapters[1].Segments, []Segment{{SourceID: original.Sources[0].ID, StartUS: 9_000_000, EndUS: 10_000_000}, {SilenceUS: 2_000_000}, {SourceID: original.Sources[1].ID, EndUS: 10_000_000}}) {
		t.Fatal("multi-segment transfer changed order")
	}
}

func TestInvalidEditsLeaveOriginal(t *testing.T) {
	type edit func(BookProject) (BookProject, error)
	cases := []struct {
		name string
		run  func(BookProject, string, string) (BookProject, error)
	}{
		{"blank title", func(p BookProject, a, b string) (BookProject, error) { return RenameChapter(p, b, " \t ") }},
		{"unknown rename", func(p BookProject, a, b string) (BookProject, error) { return RenameChapter(p, "missing", "C") }},
		{"unknown move", func(p BookProject, a, b string) (BookProject, error) { return MoveChapter(p, "missing", 0) }},
		{"destination negative", func(p BookProject, a, b string) (BookProject, error) { return MoveChapter(p, b, -1) }},
		{"destination past end", func(p BookProject, a, b string) (BookProject, error) { return MoveChapter(p, b, 2) }},
		{"first boundary", func(p BookProject, a, b string) (BookProject, error) { return ShiftBoundary(p, a, 1, "transfer") }},
		{"first no-op", func(p BookProject, a, b string) (BookProject, error) { return ShiftBoundary(p, a, 0, "transfer") }},
		{"previous start", func(p BookProject, a, b string) (BookProject, error) { return ShiftBoundary(p, b, 0, "transfer") }},
		{"current end", func(p BookProject, a, b string) (BookProject, error) {
			return ShiftBoundary(p, b, 20_000_000, "transfer")
		}},
		{"backward remove", func(p BookProject, a, b string) (BookProject, error) { return ShiftBoundary(p, b, 9_000_000, "remove") }},
		{"unchanged remove", func(p BookProject, a, b string) (BookProject, error) {
			return ShiftBoundary(p, b, 10_000_000, "remove")
		}},
		{"unknown boundary", func(p BookProject, a, b string) (BookProject, error) {
			return ShiftBoundary(p, "missing", 11_000_000, "transfer")
		}},
		{"bad mode", func(p BookProject, a, b string) (BookProject, error) { return ShiftBoundary(p, b, 11_000_000, "bad") }},
		{"bad no-op mode", func(p BookProject, a, b string) (BookProject, error) { return ShiftBoundary(p, b, 10_000_000, "bad") }},
		{"bad position", func(p BookProject, a, b string) (BookProject, error) { return AddSilence(p, b, 1, "middle") }},
		{"zero silence", func(p BookProject, a, b string) (BookProject, error) { return AddSilence(p, b, 0, "start") }},
		{"negative silence", func(p BookProject, a, b string) (BookProject, error) { return AddSilence(p, b, -1, "end") }},
		{"unknown silence", func(p BookProject, a, b string) (BookProject, error) { return AddSilence(p, "missing", 1, "start") }},
		{"silence overflow", func(p BookProject, a, b string) (BookProject, error) { return AddSilence(p, b, math.MaxInt64, "start") }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			original := projectFixture(t)
			before := Clone(original)
			result, err := test.run(original, original.Chapters[0].ID, original.Chapters[1].ID)
			if err == nil || !reflect.DeepEqual(result, BookProject{}) {
				t.Fatalf("invalid edit result=%+v err=%v", result, err)
			}
			if !reflect.DeepEqual(original, before) {
				t.Fatal("failed edit mutated original")
			}
		})
	}
	for name, run := range map[string]edit{
		"rename": func(p BookProject) (BookProject, error) { return RenameChapter(p, p.Chapters[1].ID, "C") },
		"move":   func(p BookProject) (BookProject, error) { return MoveChapter(p, p.Chapters[1].ID, 0) },
		"shift": func(p BookProject) (BookProject, error) {
			return ShiftBoundary(p, p.Chapters[1].ID, 11_000_000, "transfer")
		},
		"silence": func(p BookProject) (BookProject, error) { return AddSilence(p, p.Chapters[1].ID, 1, "start") },
	} {
		t.Run("invalid input "+name, func(t *testing.T) {
			p := projectFixture(t)
			p.SchemaVersion = 0
			before := Clone(p)
			result, err := run(p)
			if err == nil || !reflect.DeepEqual(result, BookProject{}) || !reflect.DeepEqual(p, before) {
				t.Fatal("invalid input was accepted or changed")
			}
		})
	}
}
