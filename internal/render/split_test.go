package render

import (
	"context"
	"reflect"
	"testing"

	"chaptersmith/internal/book"
)

const sec = 44100

// Pauses in a 60 s recording: 2 s at 10 s, 0.6 s at 20 s (too short), 3 s at
// 30 s, 1.5 s at 34 s (too close to the longer one) and 1.5 s at the very start.
var splitPauses = silentRuns([2]int64{0, 1.5 * sec}, [2]int64{10 * sec, 12 * sec}, [2]int64{20 * sec, 20.6 * sec}, [2]int64{30 * sec, 33 * sec}, [2]int64{34 * sec, 35.5 * sec})
var splitDefaults = book.SplitOptions{MinSilenceUS: 1e6, ThresholdDB: -40, MinChapterUS: 5e6}

func TestSuggestSplitsProposesTheMiddleOfLongPauses(t *testing.T) {
	p := silenceProject(t, 60, 1, nil, splitPauses)
	before := book.Clone(p)
	var last, total int64
	got, err := SuggestSplits(context.Background(), p, splitDefaults, func(done, all int64) { last, total = done, all })
	if err != nil {
		t.Fatal(err)
	}
	want := []book.SplitProposal{{AtUS: 11e6, SilenceUS: 2e6}, {AtUS: 31.5e6, SilenceUS: 3e6}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proposals %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("suggesting splits changed the project")
	}
	if last != 60e6 || total != 60e6 {
		t.Fatalf("progress ended at %d of %d", last, total)
	}
	split, err := book.SplitChapters(p, []int64{got[0].AtUS, got[1].AtUS})
	if err != nil || len(split.Chapters) != 3 {
		t.Fatalf("proposals did not apply: %v, %d chapters", err, len(split.Chapters))
	}
}

func TestSuggestSplitsKeepsExistingChapterStarts(t *testing.T) {
	// A chapter already starts at 11 s, inside the first pause.
	p := silenceProject(t, 60, 1, []int64{11e6}, splitPauses)
	got, err := SuggestSplits(context.Background(), p, splitDefaults, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []book.SplitProposal{{AtUS: 31.5e6, SilenceUS: 3e6}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("proposals %+v, want %+v", got, want)
	}
}

func TestSuggestSplitsWithoutMinimumChapterLengthKeepsEveryPause(t *testing.T) {
	p := silenceProject(t, 60, 1, nil, splitPauses)
	opts := splitDefaults
	opts.MinChapterUS = 0
	got, err := SuggestSplits(context.Background(), p, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].AtUS != 0.75e6 || got[3].AtUS != 34.75e6 {
		t.Fatalf("proposals %+v", got)
	}
}

func TestSuggestSplitsRejectsBadOptionsAndCancellation(t *testing.T) {
	p := silenceProject(t, 5, 1, nil, splitPauses)
	for _, opts := range []book.SplitOptions{{}, {MinSilenceUS: 1e6, ThresholdDB: 1}, {MinSilenceUS: 1e6, ThresholdDB: -40, MinChapterUS: -1}} {
		if _, err := SuggestSplits(context.Background(), p, opts, nil); err == nil {
			t.Errorf("options %+v were accepted", opts)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SuggestSplits(ctx, p, splitDefaults, nil); err == nil {
		t.Error("a cancelled search returned proposals")
	}
}
