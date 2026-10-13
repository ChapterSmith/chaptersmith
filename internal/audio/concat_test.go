package audio

import (
	"context"
	"errors"
	"io"
	"log"
	"math"
	"path/filepath"
	"testing"

	"github.com/asticode/go-astiav"
)

func countAudioPackets(t *testing.T, path string) int {
	t.Helper()
	fc := astiav.AllocFormatContext()
	defer fc.Free()
	if err := fc.OpenInput(path, nil, nil); err != nil {
		t.Fatal(err)
	}
	defer fc.CloseInput()
	if err := fc.FindStreamInfo(nil); err != nil {
		t.Fatal(err)
	}
	audioIndex := -1
	for _, stream := range fc.Streams() {
		if stream.CodecParameters().MediaType() == astiav.MediaTypeAudio {
			audioIndex = stream.Index()
			break
		}
	}
	if audioIndex < 0 {
		t.Fatal("no audio stream")
	}
	pkt := astiav.AllocPacket()
	defer pkt.Free()
	count := 0
	for {
		if err := fc.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				return count
			}
			t.Fatal(err)
		}
		if pkt.StreamIndex() == audioIndex {
			count++
		}
		pkt.Unref()
	}
}

func TestConcatPreservesSingleSourcePackets(t *testing.T) {
	inputs := []string{"../../testdata/formats/m4a/01.m4a"}
	want := countAudioPackets(t, inputs[0])
	out := filepath.Join(t.TempDir(), "out.m4a")
	if err := Concat(context.Background(), inputs, out, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	if got := countAudioPackets(t, out); got != want {
		t.Fatalf("unexpected audio packet count after skipping subsequent AAC priming: got %d want %d", got, want)
	}
}

func TestJoinAACDoesNotAccumulateAACPrimingDelay(t *testing.T) {
	inputs := make([]string, 100)
	for i := range inputs {
		inputs[i] = "../../testdata/formats/m4a/01.m4a"
	}
	out := filepath.Join(t.TempDir(), "out.m4a")
	if _, err := JoinAAC(context.Background(), inputs, out, nil); err != nil {
		t.Fatal(err)
	}
	duration, err := GetDuration(out)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(duration-300) > 0.01 {
		t.Fatalf("AAC priming accumulated: duration %.6fs, want 300s", duration)
	}
}
