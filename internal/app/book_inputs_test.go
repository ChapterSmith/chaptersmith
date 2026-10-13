package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/asticode/go-astiav"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/muxer"
)

func cliConfig(dir string) Config {
	return Config{InputDir: dir, OutputPath: filepath.Join(dir, "out.m4b"), CPUCores: 2, Parallelism: 2, Progress: &ProgressReporter{OnLog: func(string) {}}}
}
func runCLI(cfg Config) error { return New(cfg, log.New(io.Discard, "", 0)).Run(context.Background()) }
func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func assertCLITitles(t *testing.T, path string, titles ...string) []chapter.Chapter {
	t.Helper()
	chapters, err := chapter.ReadEmbeddedChapters(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range chapters {
		got = append(got, c.Title)
	}
	if !reflect.DeepEqual(got, titles) {
		t.Fatalf("chapter titles %v, want %v", got, titles)
	}
	return chapters
}

// This test catches sorting a playlist or losing occurrence-specific EXTINF titles.
func TestCLIPlaylistOrderAndRepeatedTitles(t *testing.T) {
	dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3")
	writeCLIFile(t, filepath.Join(dir, "book.m3u"), "#EXTM3U\n#EXTINF:3,Second\n02.mp3\n#EXTINF:3,First\n01.mp3\n#EXTINF:3,Encore\n02.mp3\n")
	cfg := cliConfig(dir)
	if err := runCLI(cfg); err != nil {
		t.Fatal(err)
	}
	assertCLITitles(t, cfg.OutputPath, "Second", "First", "Encore")
	assertCLITones(t, cfg.OutputPath, []float64{660, 330, 660})
	duration, err := audio.GetDuration(cfg.OutputPath)
	if err != nil || math.Abs(duration-9) > 0.5 {
		t.Fatalf("duration=%v err=%v, want approximately 9s", duration, err)
	}
}
func embeddedCLIFiles(t *testing.T, count int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 1; i <= count; i++ {
		if err := muxer.Finalize(context.Background(), muxer.Options{InputFile: filepath.Join(formatsDir, "m4a/01.m4a"), OutputFile: filepath.Join(dir, fmt.Sprintf("%02d.m4b", i)), HasChapters: true, Chapters: []chapter.Chapter{{Title: fmt.Sprintf("%d Start", i), StartTime: 0}, {Title: fmt.Sprintf("%d Next", i), StartTime: 1}}}, log.New(io.Discard, "", 0)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// This catches using whole-file chapters when several files have embedded tables.
func TestCLIMultipleEmbeddedFiles(t *testing.T) {
	dir := embeddedCLIFiles(t, 2)
	cfg := cliConfig(dir)
	duration, err := audio.GetDuration(filepath.Join(dir, "01.m4b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runCLI(cfg); err != nil {
		t.Fatal(err)
	}
	chapters := assertCLITitles(t, cfg.OutputPath, "1 Start", "1 Next", "2 Start", "2 Next")
	if math.Abs(chapters[2].StartTime-duration) > 0.01 {
		t.Fatalf("second-source offset %v, want %v", chapters[2].StartTime, duration)
	}
}
func cliCue(first, second string) string {
	return fmt.Sprintf("FILE %q WAVE\n TRACK 01 AUDIO\n TITLE %q\n INDEX 01 00:00:00\n TRACK 02 AUDIO\n TITLE %q\n INDEX 01 00:01:00\nFILE %q WAVE\n TRACK 03 AUDIO\n TITLE %q\n INDEX 01 00:00:00\n TRACK 04 AUDIO\n TITLE %q\n INDEX 01 00:01:00\n", first, "Second start", "Second next", second, "First start", "First next")
}

// Package cwd differs from the fixture folder; no process-wide chdir is needed.
func TestCLICueUsesInputDirectory(t *testing.T) {
	dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3")
	writeCLIFile(t, filepath.Join(dir, "book.cue"), cliCue("02.mp3", "01.mp3"))
	cfg := cliConfig(dir)
	if err := runCLI(cfg); err != nil {
		t.Fatal(err)
	}
	chapters := assertCLITitles(t, cfg.OutputPath, "Second start", "Second next", "First start", "First next")
	assertCLITones(t, cfg.OutputPath, []float64{660, 330})
	if math.Abs(chapters[2].StartTime-3) > 0.1 {
		t.Fatalf("CUE second-source offset %v, want ~3s", chapters[2].StartTime)
	}
}
func TestCLIAmbiguousSelection(t *testing.T) {
	t.Run("playlists", func(t *testing.T) {
		dir := copyFixtures(t, "mp3/01.mp3")
		writeCLIFile(t, filepath.Join(dir, "a.m3u"), "01.mp3\n")
		writeCLIFile(t, filepath.Join(dir, "b.m3u"), "01.mp3\n")
		err := runCLI(cliConfig(dir))
		if err == nil || !strings.Contains(err.Error(), "a.m3u") || !strings.Contains(err.Error(), "b.m3u") {
			t.Fatalf("expected playlist candidate error, got %v", err)
		}
	})
	t.Run("cues", func(t *testing.T) {
		dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3")
		writeCLIFile(t, filepath.Join(dir, "a.cue"), cliCue("02.mp3", "01.mp3"))
		writeCLIFile(t, filepath.Join(dir, "b.cue"), cliCue("01.mp3", "02.mp3"))
		err := runCLI(cliConfig(dir))
		if err == nil || !strings.Contains(err.Error(), "a.cue") || !strings.Contains(err.Error(), "b.cue") {
			t.Fatalf("expected CUE candidate error, got %v", err)
		}
	})
}
func TestCLIPreservesSourceCover(t *testing.T) {
	for _, fix := range []bool{false, true} {
		t.Run(fmt.Sprintf("fix=%v", fix), func(t *testing.T) {
			dir := copyFixtures(t, "mp3/01.mp3", "cover.jpg")
			paths := []string{filepath.Join(dir, "cover.jpg"), filepath.Join(dir, "01.mp3")}
			var hashes [][32]byte
			for _, p := range paths {
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				hashes = append(hashes, sha256.Sum256(b))
			}
			out := runApp(t, dir, fix)
			for i, p := range paths {
				b, err := os.ReadFile(p)
				if err != nil || sha256.Sum256(b) != hashes[i] {
					t.Fatalf("source changed: %s: %v", p, err)
				}
			}
			if !hasCoverArt(t, out) {
				t.Fatal("output cover missing")
			}
		})
	}
}
func TestCLIOutputAliasesSource(t *testing.T) {
	for _, alias := range []string{"path", "symlink", "hardlink"} {
		t.Run(alias, func(t *testing.T) {
			dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3")
			source := filepath.Join(dir, "01.mp3")
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			out := source
			switch alias {
			case "symlink":
				out = filepath.Join(dir, "alias.m4b")
				if err := os.Symlink(source, out); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			case "hardlink":
				out = filepath.Join(dir, "alias.m4b")
				if err := os.Link(source, out); err != nil {
					t.Fatal(err)
				}
			}
			writeCLIFile(t, filepath.Join(dir, "book.m3u"), "01.mp3\n02.mp3\n")
			cfg := cliConfig(dir)
			cfg.OutputPath = out
			var phases []string
			cfg.Progress.OnPhase = func(p string) { phases = append(phases, p) }
			err = runCLI(cfg)
			if err == nil || !strings.Contains(err.Error(), "source") {
				t.Errorf("expected source alias rejection, got %v", err)
			}
			after, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Errorf("source overwritten: %v", readErr)
			}
			if len(phases) > 1 {
				t.Errorf("alias validation ran too late: %v", phases)
			}
		})
	}
}

// Count positive zero crossings in the middle of each committed sine fixture.
// Reading decoded audio catches a sorted playlist even when EXTINF still looks right.
func assertCLITones(t *testing.T, path string, want []float64) {
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
	stream, codec, err := fc.FindBestStream(astiav.MediaTypeAudio, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	dec := astiav.AllocCodecContext(codec)
	defer dec.Free()
	if err := stream.CodecParameters().ToCodecContext(dec); err != nil {
		t.Fatal(err)
	}
	if err := dec.Open(codec, nil); err != nil {
		t.Fatal(err)
	}
	packet := astiav.AllocPacket()
	defer packet.Free()
	frame := astiav.AllocFrame()
	defer frame.Free()
	var samples []float32
	drain := func() {
		for {
			err := dec.ReceiveFrame(frame)
			if errors.Is(err, astiav.ErrEagain) || errors.Is(err, astiav.ErrEof) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if frame.SampleFormat() != astiav.SampleFormatFltp || frame.ChannelLayout().Channels() != 1 {
				t.Fatalf("unexpected fixture audio %s", frame.SampleFormat().Name())
			}
			size, err := frame.SamplesBufferSize(1)
			if err != nil {
				t.Fatal(err)
			}
			b := make([]byte, size)
			if _, err := frame.SamplesCopyToBuffer(b, 1); err != nil {
				t.Fatal(err)
			}
			for i := 0; i+4 <= len(b); i += 4 {
				samples = append(samples, math.Float32frombits(binary.LittleEndian.Uint32(b[i:i+4])))
			}
			frame.Unref()
		}
	}
	for {
		err := fc.ReadFrame(packet)
		if errors.Is(err, astiav.ErrEof) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if packet.StreamIndex() == stream.Index() {
			if err := dec.SendPacket(packet); err != nil {
				t.Fatal(err)
			}
			drain()
		}
		packet.Unref()
	}
	if err := dec.SendPacket(nil); err != nil {
		t.Fatal(err)
	}
	drain()
	rate := dec.SampleRate()
	for part, freq := range want {
		start := (part*3 + 1) * rate
		end := start + rate/2
		if end >= len(samples) {
			t.Fatal("too little decoded audio")
		}
		crossings := 0
		for i := start + 1; i < end; i++ {
			if samples[i-1] <= 0 && samples[i] > 0 {
				crossings++
			}
		}
		got := float64(crossings) * 2
		if math.Abs(got-freq) > 4 {
			t.Errorf("occurrence %d frequency %.0fHz, want %.0fHz", part+1, got, freq)
		}
	}
}

func TestCLIExplicitSelections(t *testing.T) {
	t.Run("playlist", func(t *testing.T) {
		dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3")
		writeCLIFile(t, filepath.Join(dir, "a.m3u"), "#EXTINF:3,Selected\n02.mp3\n")
		writeCLIFile(t, filepath.Join(dir, "b.m3u"), "01.mp3\n")
		cfg := cliConfig(dir)
		cfg.PlaylistPath = filepath.Join(dir, "a.m3u")
		if err := runCLI(cfg); err != nil {
			t.Fatal(err)
		}
		assertCLITitles(t, cfg.OutputPath, "Selected")
	})
	t.Run("cue", func(t *testing.T) {
		dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3")
		writeCLIFile(t, filepath.Join(dir, "a.cue"), cliCue("02.mp3", "01.mp3"))
		writeCLIFile(t, filepath.Join(dir, "b.cue"), cliCue("01.mp3", "02.mp3"))
		cfg := cliConfig(dir)
		cfg.CuePath = filepath.Join(dir, "a.cue")
		if err := runCLI(cfg); err != nil {
			t.Fatal(err)
		}
		assertCLITitles(t, cfg.OutputPath, "Second start", "Second next", "First start", "First next")
	})
	for _, fix := range []bool{false, true} {
		t.Run(fmt.Sprintf("embedded-conflict-fix=%v", fix), func(t *testing.T) {
			dir := embeddedCLIFiles(t, 1)
			cue := filepath.Join(dir, "book.cue")
			writeCLIFile(t, cue, "FILE \"01.m4b\" WAVE\n TRACK 01 AUDIO\n TITLE \"Cue start\"\n INDEX 01 00:00:00\n TRACK 02 AUDIO\n TITLE \"Cue next\"\n INDEX 01 00:02:00\n")
			cfg := cliConfig(dir)
			cfg.FixMode = fix
			var phases []string
			cfg.Progress.OnPhase = func(p string) { phases = append(phases, p) }
			err := runCLI(cfg)
			if err == nil || !strings.Contains(err.Error(), "conflicts with embedded") {
				t.Fatalf("auto should report CUE/embedded conflict: %v", err)
			}
			for _, phase := range phases {
				if strings.Contains(phase, "CONVERT") || strings.Contains(phase, "CONVERSION") {
					t.Fatalf("encoded before chapter selection: %v", phases)
				}
			}
			cfg.CuePath = cue
			if err := runCLI(cfg); err != nil {
				t.Fatal(err)
			}
			assertCLITitles(t, cfg.OutputPath, "Cue start", "Cue next")
			cfg.CuePath = ""
			cfg.ChapterSource = "embedded"
			if err := runCLI(cfg); err != nil {
				t.Fatal(err)
			}
			assertCLITitles(t, cfg.OutputPath, "1 Start", "1 Next")
			cfg.ChapterSource = "files"
			if err := runCLI(cfg); err != nil {
				t.Fatal(err)
			}
			assertCLITitles(t, cfg.OutputPath, "01")
			cfg.CuePath = cue
			if err := runCLI(cfg); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("explicit CUE/files should conflict: %v", err)
			}
		})
	}
}

func TestCLIFixOutputSafety(t *testing.T) {
	t.Run("same-path-renames", func(t *testing.T) {
		dir := copyFixtures(t, "mp3/01.mp3")
		source := filepath.Join(dir, "01.mp3")
		before, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		writeCLIFile(t, filepath.Join(dir, "book.m3u"), "01.mp3\n")
		cfg := cliConfig(dir)
		cfg.FixMode = true
		cfg.OutputPath = source
		if err := runCLI(cfg); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(source)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("source changed: %v", err)
		}
		assertCLITitles(t, filepath.Join(dir, "01_fixed.m4b"), "01")
	})
	for _, alias := range []string{"existing-fixed", "hardlink-fixed", "symlink-original", "hardlink-original"} {
		t.Run(alias, func(t *testing.T) {
			dir := copyFixtures(t, "mp3/01.mp3")
			source := filepath.Join(dir, "01.mp3")
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			writeCLIFile(t, filepath.Join(dir, "book.m3u"), "01.mp3\n")
			cfg := cliConfig(dir)
			cfg.FixMode = true
			cfg.OutputPath = source
			destination := filepath.Join(dir, "01_fixed.m4b")
			switch alias {
			case "existing-fixed":
				writeCLIFile(t, destination, "existing output")
			case "hardlink-fixed":
				if err := os.Link(source, destination); err != nil {
					t.Fatal(err)
				}
			case "symlink-original":
				cfg.OutputPath = filepath.Join(dir, "alias.mp3")
				if err := os.Symlink(source, cfg.OutputPath); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
				destination = cfg.OutputPath
			case "hardlink-original":
				cfg.OutputPath = filepath.Join(dir, "alias.mp3")
				if err := os.Link(source, cfg.OutputPath); err != nil {
					t.Fatal(err)
				}
				destination = cfg.OutputPath
			}
			destBefore, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			err = runCLI(cfg)
			if err == nil {
				t.Error("expected unsafe fix destination rejection")
			}
			after, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Errorf("source changed: %v", readErr)
			}
			destAfter, readErr := os.ReadFile(destination)
			if readErr != nil || !bytes.Equal(destBefore, destAfter) {
				t.Errorf("destination changed: %v", readErr)
			}
		})
	}
}

func TestCLIMalformedChaptersFailBeforeEncoding(t *testing.T) {
	dir := copyFixtures(t, "mp3/01.mp3")
	writeCLIFile(t, filepath.Join(dir, "book.cue"), "FILE \"01.mp3\" WAVE\n TRACK 01 AUDIO\n INDEX 01 00:10:00\n")
	for _, fix := range []bool{false, true} {
		cfg := cliConfig(dir)
		cfg.FixMode = fix
		var phases []string
		cfg.Progress.OnPhase = func(p string) { phases = append(phases, p) }
		err := runCLI(cfg)
		if err == nil || !strings.Contains(err.Error(), "out-of-bounds") {
			t.Fatalf("expected chapter bounds error: %v", err)
		}
		for _, phase := range phases {
			if strings.Contains(phase, "CONVERT") || strings.Contains(phase, "CONVERSION") {
				t.Fatalf("encoded before chapter validation: %v", phases)
			}
		}
		if _, err := os.Stat(cfg.OutputPath); !os.IsNotExist(err) {
			t.Fatalf("wrote output before chapter validation: %v", err)
		}
	}
}

func TestCLIRepeatedEmbeddedSources(t *testing.T) {
	dir := embeddedCLIFiles(t, 2)
	writeCLIFile(t, filepath.Join(dir, "book.m3u"), `#EXTINF:3,Ignored second
02.m4b
#EXTINF:3,Ignored first
01.m4b
#EXTINF:3,Ignored encore
02.m4b
`)
	cfg := cliConfig(dir)
	if err := runCLI(cfg); err != nil {
		t.Fatal(err)
	}
	chapters := assertCLITitles(t, cfg.OutputPath, "2 Start", "2 Next", "1 Start", "1 Next", "2 Start", "2 Next")
	if math.Abs(chapters[4].StartTime-6) > 0.02 {
		t.Fatalf("repeated occurrence offset %v, want ~6s", chapters[4].StartTime)
	}
}

func TestCLIExactOutputDoesNotTouchSameStemSource(t *testing.T) {
	for _, fix := range []bool{false, true} {
		t.Run(fmt.Sprintf("fix=%v", fix), func(t *testing.T) {
			dir := embeddedCLIFiles(t, 1)
			source := filepath.Join(dir, "01.m4b")
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			writeCLIFile(t, filepath.Join(dir, "book.m3u"), `01.m4b
`)
			cfg := cliConfig(dir)
			cfg.FixMode = fix
			cfg.OutputPath = filepath.Join(dir, "01.mp3")
			err = runCLI(cfg)
			if err != nil {
				t.Fatalf("exact output path was rejected: %v", err)
			}
			after, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Errorf("source changed: %v", readErr)
			}
			if _, err := os.Stat(cfg.OutputPath); err != nil {
				t.Errorf("selected output missing: %v", err)
			}
		})
	}
}

// A managed cover copy does not make the original cover a safe output target.
func TestCLIOutputAliasesSourceCover(t *testing.T) {
	for _, fix := range []bool{false, true} {
		for _, alias := range []string{"direct", "symlink-write", "hardlink-write"} {
			t.Run(fmt.Sprintf("fix=%v/%s", fix, alias), func(t *testing.T) {
				dir := copyFixtures(t, "mp3/01.mp3", "cover.jpg")
				cover := filepath.Join(dir, "cover.jpg")
				before, err := os.ReadFile(cover)
				if err != nil {
					t.Fatal(err)
				}
				writeCLIFile(t, filepath.Join(dir, "book.m3u"), "01.mp3\n")
				cfg := cliConfig(dir)
				cfg.FixMode = fix
				cfg.OutputPath = cover
				if alias != "direct" {
					destination := filepath.Join(dir, "alias.m4b")
					if strings.HasPrefix(alias, "symlink") {
						if err := os.Symlink(cover, destination); err != nil {
							t.Skipf("symlink unsupported: %v", err)
						}
					} else if err := os.Link(cover, destination); err != nil {
						t.Fatal(err)
					}
					cfg.OutputPath = destination
					if strings.HasSuffix(alias, "rename") {
						cfg.OutputPath = filepath.Join(dir, "alias.mp3")
					}
				}
				var phases []string
				cfg.Progress.OnPhase = func(phase string) { phases = append(phases, phase) }
				err = runCLI(cfg)
				if err == nil || !strings.Contains(err.Error(), "source") {
					t.Errorf("expected source cover alias rejection, got %v", err)
				}
				after, readErr := os.ReadFile(cover)
				if readErr != nil || !bytes.Equal(before, after) {
					t.Errorf("source cover changed: %v", readErr)
				}
				for _, phase := range phases {
					if strings.Contains(phase, "FINALIZ") {
						t.Errorf("reached output write before cover alias validation: %v", phases)
					}
				}
			})
		}
	}
}
