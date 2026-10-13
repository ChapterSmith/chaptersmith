package render

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"chaptersmith/internal/book"
)

// Every source frame holds its own number, so a misplaced sample is visible.
func streamSample(frame int64, _ int) float32 { return float32(frame%20001-10000) / 32767 }
func streamValue(frame int64) int16           { return int16(frame%20001 - 10000) }

// streamFixture is a 30 s source whose chapter "c" plays 20–25 s, one second
// of inserted silence, then 25–30 s.
func streamFixture(t *testing.T) book.BookProject {
	t.Helper()
	p := silenceProject(t, 30, 1, []int64{10e6, 20e6}, streamSample)
	p.Chapters[2].Segments = []book.Segment{
		{SourceID: "s", StartUS: 20e6, EndUS: 25e6},
		{SilenceUS: 1e6},
		{SourceID: "s", StartUS: 25e6, EndUS: 30e6},
	}
	return p
}

func streamExpected() []int16 {
	var want []int16
	for f := int64(20 * 44100); f < 25*44100; f++ {
		want = append(want, streamValue(f))
	}
	want = append(want, make([]int16, 44100)...)
	for f := int64(25 * 44100); f < 30*44100; f++ {
		want = append(want, streamValue(f))
	}
	return want
}

func TestChapterStreamIsTheEditedChapterAsWAV(t *testing.T) {
	s, err := OpenChapterStream(streamFixture(t), "c")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := streamExpected()
	if s.Size() != int64(44+2*len(want)) {
		t.Fatalf("size %d, want %d", s.Size(), 44+2*len(want))
	}
	file, err := io.ReadAll(io.NewSectionReader(s, 0, s.Size()))
	if err != nil {
		t.Fatal(err)
	}
	if string(file[:4]) != "RIFF" || string(file[8:16]) != "WAVEfmt " || string(file[36:40]) != "data" {
		t.Fatalf("not a WAV header: %q", file[:44])
	}
	if rate, channels, bits := binary.LittleEndian.Uint32(file[24:]), binary.LittleEndian.Uint16(file[22:]), binary.LittleEndian.Uint16(file[34:]); rate != 44100 || channels != 1 || bits != 16 {
		t.Fatalf("format %d Hz, %d channels, %d bits", rate, channels, bits)
	}
	if got := binary.LittleEndian.Uint32(file[40:]); int(got) != 2*len(want) {
		t.Fatalf("data length %d, want %d", got, 2*len(want))
	}
	for i, v := range want {
		if got := int16(binary.LittleEndian.Uint16(file[44+2*i:])); got != v {
			t.Fatalf("frame %d is %d, want %d", i, got, v)
		}
	}
}

func TestChapterStreamJumpsToAnySampleInAnyOrder(t *testing.T) {
	s, err := OpenChapterStream(streamFixture(t), "c")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := streamExpected()
	wantBytes := make([]byte, 44, 44+2*len(want))
	if _, err = s.ReadAt(wantBytes, 0); err != nil {
		t.Fatal(err)
	}
	for _, v := range want {
		wantBytes = binary.LittleEndian.AppendUint16(wantBytes, uint16(v))
	}
	// Late, then early, across the silence, and starting in the middle of a frame.
	for _, at := range []int64{44 + 2*9*44100, 44 + 2*100, 44 + 2*5*44100 - 300, 44 + 2*7*44100 + 1, 3, s.Size() - 10} {
		got := make([]byte, min(1001, s.Size()-at))
		if _, err := s.ReadAt(got, at); err != nil {
			t.Fatalf("offset %d: %v", at, err)
		}
		if string(got) != string(wantBytes[at:at+int64(len(got))]) {
			t.Fatalf("bytes at offset %d differ from the sequential stream", at)
		}
	}
	if n, err := s.ReadAt(make([]byte, 20), s.Size()-10); n != 10 || !errors.Is(err, io.EOF) {
		t.Fatalf("read past the end returned %d, %v", n, err)
	}
}

func TestChapterStreamRejectsUnknownChapterAndUseAfterClose(t *testing.T) {
	p := streamFixture(t)
	if _, err := OpenChapterStream(p, "missing"); err == nil {
		t.Fatal("unknown chapter was accepted")
	}
	s, err := OpenChapterStream(p, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadAt(make([]byte, 8), 44); err == nil {
		t.Fatal("read after Close succeeded")
	}
	if err = s.Close(); err != nil {
		t.Fatal("second Close:", err)
	}
}
