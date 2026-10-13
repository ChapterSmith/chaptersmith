package render

import (
	"context"
	"reflect"
	"testing"

	"chaptersmith/internal/book"
)

func TestMeasureChapterPausesFindsTheSilenceAroundEachChapterStart(t *testing.T) {
	// Chapter b starts at 10 s inside 9–10.5 s of silence; chapter c starts at
	// 20 s with speech right up to it and half a second of silence after.
	quiet := silentRuns([2]int64{9 * sec, 10.5 * sec}, [2]int64{20 * sec, 20.5 * sec}, [2]int64{25 * sec, 26 * sec})
	p := silenceProject(t, 30, 1, []int64{10e6, 20e6}, quiet)
	before := book.Clone(p)
	got, err := MeasureChapterPauses(context.Background(), p, -40, 5e6)
	if err != nil {
		t.Fatal(err)
	}
	want := []book.ChapterPause{{ChapterID: "b", BeforeUS: 1e6, AfterUS: 5e5}, {ChapterID: "c", BeforeUS: 0, AfterUS: 5e5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pauses %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("measuring changed the project")
	}
	// The measurement and the edit together leave exactly the chosen pause.
	set, err := book.SetChapterPauses(p, got, 2e6)
	if err != nil {
		t.Fatal(err)
	}
	if book.DurationUS(set) != 30e6-1.5e6-0.5e6+2*2e6 {
		t.Fatalf("book is %d µs after replacing the pauses", book.DurationUS(set))
	}
}

func TestMeasureChapterPausesLooksNoFurtherThanTheWindowAndReportsNoneWhereThereIsSpeech(t *testing.T) {
	quiet := silentRuns([2]int64{5 * sec, 10 * sec})
	p := silenceProject(t, 30, 1, []int64{10e6, 20e6}, quiet)
	got, err := MeasureChapterPauses(context.Background(), p, -40, 2e6)
	if err != nil {
		t.Fatal(err)
	}
	want := []book.ChapterPause{{ChapterID: "b", BeforeUS: 2e6}, {ChapterID: "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pauses %+v, want %+v", got, want)
	}
	if _, err = MeasureChapterPauses(context.Background(), p, 3, 2e6); err == nil {
		t.Fatal("a positive threshold was accepted")
	}
}
