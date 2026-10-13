package audio

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

const sampleAudioPath = "../../testdata/formats/m4a/01.m4a"

// shortInputs cuts n two-second clips from the sample audiobook.
func shortInputs(t *testing.T, n int) []string {
	t.Helper()
	dir := t.TempDir()
	files := make([]string, n)
	for i := range n {
		files[i] = filepath.Join(dir, fmt.Sprintf("in_%d.m4a", i))
		start := 0.0
		opts := TranscodeOptions{StartSec: start, EndSec: start + 2}
		if err := TranscodeFile(context.Background(), sampleAudioPath, files[i], opts); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestTranscodeParallelReportsProgressForEveryFile(t *testing.T) {
	inputs := shortInputs(t, 4)
	var calls []int

	out, err := TranscodeParallel(context.Background(), inputs, t.TempDir(), len(inputs), len(inputs),
		func(current, total int) { calls = append(calls, current) }, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(inputs) {
		t.Fatalf("got %d outputs, want %d", len(out), len(inputs))
	}
	if len(calls) != len(inputs) {
		t.Fatalf("got %d progress calls, want %d", len(calls), len(inputs))
	}
}

func TestTranscodeParallelCancelledMidwayReturnsError(t *testing.T) {
	inputs := shortInputs(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One worker: cancelling after the first file stops the remaining jobs
	// from starting while the first one completed successfully.
	out, err := TranscodeParallel(ctx, inputs, t.TempDir(), 1, 1,
		func(current, total int) { cancel() }, false)
	if err == nil {
		t.Fatalf("expected an error after cancellation, got %d of %d files", len(out), len(inputs))
	}
}
