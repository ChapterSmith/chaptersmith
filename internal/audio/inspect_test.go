package audio

import (
	"chaptersmith/internal/audiotest"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectPrimingAndTimestamps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "origin.m4a")
	audiotest.WriteAAC(t, p, 44117, 88200)
	var problems []PCMProblem
	got, e := InspectPCM(context.Background(), p, 1, 0, -1, func(b []float32) error { return nil }, func(p PCMProblem) { problems = append(problems, p) })
	if e != nil || !got.Complete || got.EmittedFrames != 44117 || len(problems) != 0 {
		t.Fatalf("%+v %v %+v", got, e, problems)
	}
	for _, tt := range []struct {
		delta int64
		want  int
	}{{100000, 0}, {100001, 1}, {-100001, 1}} {
		var seen []PCMProblem
		tracker := timestampTracker{}
		tracker.observe(0, 1000, 2000000, 100000, true, 0, -1, func(p PCMProblem) { seen = append(seen, p) })
		tracker.observe(1000, 2000, 2100000+tt.delta, 100000, true, 0, -1, func(p PCMProblem) { seen = append(seen, p) })
		if len(seen) != tt.want {
			t.Fatalf("delta %d: %+v", tt.delta, seen)
		}
	}
	var seen []PCMProblem
	tracker := timestampTracker{}
	tracker.observe(0, 1000, 0, 100000, false, 600, -1, func(p PCMProblem) { seen = append(seen, p) })
	if len(seen) != 1 || seen[0].Kind != "missing_timestamp" || seen[0].SourceFrame != nil {
		t.Fatalf("%+v", seen)
	}
}
func TestInspectFailureClasses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.flac")
	audiotest.WriteFLAC(t, p, 44100)
	if e := os.Truncate(p, audiotest.LastPacketOffset(t, p)); e != nil {
		t.Fatal(e)
	}
	var problems []PCMProblem
	var frames int64
	got, e := InspectPCM(context.Background(), p, 1, 0, -1, func(b []float32) error { frames += int64(len(b)); return nil }, func(p PCMProblem) { problems = append(problems, p) })
	if e != nil || got.Complete || frames == 0 || got.EmittedFrames != frames || len(problems) != 1 || problems[0].Kind != "decode_error" {
		t.Fatalf("%+v %d %v %+v", got, frames, e, problems)
	}
	if e := DecodePCM(context.Background(), p, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("strict decoder accepted truncated FLAC")
	}
	fault := errors.New("sink")
	_, e = InspectPCM(context.Background(), p, 1, 0, -1, func([]float32) error { return fault }, nil)
	if !errors.Is(e, fault) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = InspectPCM(ctx, p, 1, 0, -1, nil, nil)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = InspectPCM(context.Background(), p+"missing", 1, 0, -1, nil, nil)
	if !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}
func TestInspectTruncatedWAVRetainsPrefix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 44100))
	if e := os.Truncate(p, 44+8192); e != nil {
		t.Fatal(e)
	}
	got, e := InspectPCM(context.Background(), p, 1, 0, -1, nil, nil)
	if e != nil || got.Complete || got.EmittedFrames != 4096 {
		t.Fatalf("%+v %v", got, e)
	}
}

// Independent Matroska PCM fixture uses one-microsecond timestamps, avoiding
// muxer timestamp repair and exercising the linked native decoder's AVFrames.
func writeInspectionTimestampFixture(t *testing.T, path string, timestamps []int64) {
	t.Helper()
	element := func(id []byte, payload []byte) []byte {
		b := append([]byte(nil), id...)
		size := make([]byte, 4)
		binary.BigEndian.PutUint32(size, 0x10000000|uint32(len(payload)))
		b = append(b, size...)
		return append(b, payload...)
	}
	uintElement := func(id []byte, n uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, n)
		return element(id, b)
	}
	var header []byte
	for _, f := range []struct {
		id []byte
		n  uint64
	}{{[]byte{0x42, 0x86}, 1}, {[]byte{0x42, 0xf7}, 1}, {[]byte{0x42, 0xf2}, 4}, {[]byte{0x42, 0xf3}, 8}, {[]byte{0x42, 0x87}, 4}, {[]byte{0x42, 0x85}, 2}} {
		header = append(header, uintElement(f.id, f.n)...)
	}
	header = append(header, element([]byte{0x42, 0x82}, []byte("matroska"))...)
	info := uintElement([]byte{0x2a, 0xd7, 0xb1}, 1000)
	track := uintElement([]byte{0xd7}, 1)
	track = append(track, uintElement([]byte{0x73, 0xc5}, 1)...)
	track = append(track, uintElement([]byte{0x83}, 2)...)
	track = append(track, element([]byte{0x86}, []byte("A_PCM/INT/LIT"))...)
	frequency := make([]byte, 8)
	binary.BigEndian.PutUint64(frequency, math.Float64bits(44100))
	audio := element([]byte{0xb5}, frequency)
	audio = append(audio, uintElement([]byte{0x9f}, 1)...)
	audio = append(audio, uintElement([]byte{0x62, 0x64}, 16)...)
	track = append(track, element([]byte{0xe1}, audio)...)
	segment := element([]byte{0x15, 0x49, 0xa9, 0x66}, info)
	// A real JPEG video track deliberately jumps back and forth in time.
	// Only decoded audio-frame observations may reach inspection callbacks.
	videoTrack := uintElement([]byte{0xd7}, 2)
	videoTrack = append(videoTrack, uintElement([]byte{0x73, 0xc5}, 2)...)
	videoTrack = append(videoTrack, uintElement([]byte{0x83}, 1)...)
	videoTrack = append(videoTrack, element([]byte{0x86}, []byte("V_MJPEG"))...)
	dimensions := uintElement([]byte{0xb0}, 16)
	dimensions = append(dimensions, uintElement([]byte{0xba}, 16)...)
	videoTrack = append(videoTrack, element([]byte{0xe0}, dimensions)...)
	tracks := element([]byte{0xae}, track)
	tracks = append(tracks, element([]byte{0xae}, videoTrack)...)
	segment = append(segment, element([]byte{0x16, 0x54, 0xae, 0x6b}, tracks)...)
	jpeg, e := os.ReadFile("../../testdata/formats/cover.jpg")
	if e != nil {
		t.Fatal(e)
	}
	for _, timestamp := range []int64{0, 4000000, 1000000} {
		cluster := uintElement([]byte{0xe7}, uint64(timestamp))
		block := append([]byte{0x82, 0, 0, 0x80}, jpeg...)
		cluster = append(cluster, element([]byte{0xa3}, block)...)
		segment = append(segment, element([]byte{0x1f, 0x43, 0xb6, 0x75}, cluster)...)
	}
	for _, timestamp := range timestamps {
		cluster := uintElement([]byte{0xe7}, uint64(timestamp))
		block := append([]byte{0x81, 0, 0, 0x80}, make([]byte, 882)...)
		cluster = append(cluster, element([]byte{0xa3}, block)...)
		segment = append(segment, element([]byte{0x1f, 0x43, 0xb6, 0x75}, cluster)...)
	}
	out := element([]byte{0x1a, 0x45, 0xdf, 0xa3}, header)
	out = append(out, element([]byte{0x18, 0x53, 0x80, 0x67}, segment)...)
	if e := os.WriteFile(path, out, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestInspectNativeTimestamps(t *testing.T) {
	for _, tt := range []struct {
		name       string
		pts        []int64
		start, end int64
		want       int
	}{{"boundary", []int64{1000000, 1110000, 1120000}, 0, -1, 0}, {"gap", []int64{1000000, 1110001, 1120001}, 0, -1, 1}, {"backwards", []int64{1000000, 800000, 810000}, 0, -1, 1}, {"outsideSelection", []int64{1000000, 1110001, 1120001}, 882, 1323, 0}, {"midFrameTrim600", []int64{1000000, 1110001, 1120001}, 600, 1323, 0}, {"midFrameTrim650", []int64{1000000, 1110001, 1120001}, 650, 1323, 0}, {"atSelectionStart", []int64{1000000, 1110001, 1120001}, 441, 1323, 1}, {"atSelectionEnd", []int64{1000000, 1110001, 1120001}, 0, 441, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "clock.mka")
			writeInspectionTimestampFixture(t, p, tt.pts)
			var problems []PCMProblem
			r, e := InspectPCM(context.Background(), p, 1, tt.start, tt.end, nil, func(p PCMProblem) { problems = append(problems, p) })
			if e != nil || !r.Complete || len(problems) != tt.want {
				t.Fatalf("%+v %v %+v", r, e, problems)
			}
			if tt.want > 0 && (problems[0].SourceFrame == nil || *problems[0].SourceFrame != 441 || *problems[0].OriginalTimestampUS != tt.pts[1]) {
				t.Fatalf("%+v", problems[0])
			}
		})
	}
}
func TestInspectNeverPadsShortRange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "short.wav")
	audiotest.WriteWAV(t, p, 44100, 1, []int16{1, 2})
	r, e := InspectPCM(context.Background(), p, 1, 0, 3, nil, nil)
	if e != nil || r.Complete || r.EmittedFrames != 2 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestInspectInputFailureStaysOperational(t *testing.T) {
	p := filepath.Join(t.TempDir(), "input.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 441000))
	file, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	fault := errors.New("inspection read failed")
	input := &failingInput{File: file, remaining: 300000, fault: fault}
	var problems []PCMProblem
	_, e = inspectPCMInput(context.Background(), p, 1, 0, -1, nil, func(p PCMProblem) { problems = append(problems, p) }, input)
	if !errors.Is(e, fault) || !input.closed || len(problems) != 0 {
		t.Fatalf("closed=%v err=%v problems=%+v", input.closed, e, problems)
	}
}

type inspectRestoreFailure struct {
	*os.File
	seeks  int
	fault  error
	closed bool
}

func (r *inspectRestoreFailure) Seek(offset int64, whence int) (int64, error) {
	r.seeks++
	if r.seeks == 4 {
		return 0, r.fault
	}
	return r.File.Seek(offset, whence)
}
func (r *inspectRestoreFailure) Close() error { r.closed = true; return r.File.Close() }
func TestInspectBoundsCannotHideSeekFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), "truncated.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 44100))
	if e := os.Truncate(p, 44+8192); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	fault := errors.New("restore seek failed")
	input := &inspectRestoreFailure{File: file, fault: fault}
	_, e = inspectPCMInput(context.Background(), p, 1, 0, -1, nil, nil, input)
	if !errors.Is(e, fault) || !input.closed {
		t.Fatalf("closed %v err %v", input.closed, e)
	}
}

// The demuxer can recover a valid decoded-frame clock even when the raw
// transport has no timestamps. Inspection reports decoded-frame evidence only.
func TestInspectNativeGeneratedTimestamps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raw.aac")
	audiotest.WriteRawAAC(t, p, 44100)
	var problems []PCMProblem
	r, e := InspectPCM(context.Background(), p, 1, 0, -1, nil, func(p PCMProblem) { problems = append(problems, p) })
	if e != nil || !r.Complete || r.EmittedFrames != 46080 || len(problems) != 0 {
		t.Fatalf("%+v err %v problems %+v", r, e, problems)
	}
}
