package gui

import (
	"slices"
	"testing"
	"time"

	"chaptersmith/internal/analysis"
)

func TestThrottleProgressKeepsPhaseEdgesAndDropsBursts(t *testing.T) {
	var got []analysis.Progress
	report := throttleProgress(time.Hour, func(p analysis.Progress) { got = append(got, p) })
	for i := int64(0); i <= 1000; i++ {
		report(analysis.Progress{Phase: "decode", Current: i, Total: 1000})
	}
	report(analysis.Progress{Phase: "repeat-verify", Current: 0, Total: 10})

	want := []analysis.Progress{
		{Phase: "decode", Current: 0, Total: 1000},
		{Phase: "decode", Current: 1000, Total: 1000},
		{Phase: "repeat-verify", Current: 0, Total: 10},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("forwarded %v, want %v", got, want)
	}
}

func TestThrottleProgressForwardsAgainAfterInterval(t *testing.T) {
	var got int
	report := throttleProgress(time.Millisecond, func(analysis.Progress) { got++ })
	report(analysis.Progress{Phase: "decode", Current: 1, Total: 100})
	time.Sleep(5 * time.Millisecond)
	report(analysis.Progress{Phase: "decode", Current: 2, Total: 100})
	if got != 2 {
		t.Fatalf("forwarded %d reports, want 2", got)
	}
}

func TestThrottleProgressWithoutListener(t *testing.T) {
	if throttleProgress(time.Second, nil) != nil {
		t.Fatal("a missing listener must stay nil so the engine skips reporting")
	}
}
