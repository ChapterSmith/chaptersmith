package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/meta"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func exportFixture(t *testing.T) (book.BookProject, []float32) {
	t.Helper()
	p, _ := fixtureProject(t, 44100)
	samples := make([]int16, 44100)
	for i := range samples {
		samples[i] = int16(10000 * math.Sin(2*math.Pi*440*float64(i)/44100))
	}
	audiotest.WriteWAV(t, p.Sources[0].Path, 44100, 1, samples)
	st, e := os.Stat(p.Sources[0].Path)
	if e != nil {
		t.Fatal(e)
	}
	p.Sources[0].SizeBytes = st.Size()
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Metadata = meta.AudiobookMetadata{Title: "Snapshot book", Artist: "Author", Composer: "Narrator", Extra: map[string]string{"keywords": "project", "title": "wrong", "track": "9/9"}}
	p.Chapters = []book.Chapter{
		{ID: "a", Title: "Second half", Segments: []book.Segment{{SourceID: "s", StartUS: 500000, EndUS: 1000000}, {SilenceUS: 100000}}},
		{ID: "b", Title: "First half", Segments: []book.Segment{{SourceID: "s", EndUS: 500000}}},
	}
	want := append([]float32{}, pcmFloats(samples[22050:])...)
	want = append(want, make([]float32, 4410)...)
	want = append(want, pcmFloats(samples[:22050])...)
	return p, want
}
func exportDecode(t *testing.T, path string) []float32 {
	t.Helper()
	var out []float32
	if e := audio.DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error { out = append(out, b...); return nil }); e != nil {
		t.Fatal(e)
	}
	return out
}
func exportAudioEqual(t *testing.T, got, want []float32, lossless bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("frames %d want %d", len(got), len(want))
	}
	if lossless {
		if !reflect.DeepEqual(got, want) {
			t.Fatal("lossless timeline changed")
		}
		return
	}
	var power, delta float64
	for i, v := range want {
		power += float64(v * v)
		d := float64(got[i] - v)
		delta += d * d
	}
	if delta/power > .025 {
		t.Fatalf("audio mismatch %g", delta/power)
	}
}

// Catches wrong order/silence, AAC re-encoding chapters from the book and dropped metadata/markers.
func TestExportMatrix(t *testing.T) {
	for _, mode := range []string{"book", "chapters", "both"} {
		for _, format := range []string{"m4b", "m4a", "flac", "wav"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				p, want := exportFixture(t)
				p.Export.OutputMode = mode
				p.Export.Format = format
				dst := filepath.Join(t.TempDir(), "result")
				if mode == "book" {
					dst += "." + format
				}
				result, e := Export(context.Background(), p, dst, nil, nil)
				if e != nil {
					t.Fatal(e)
				}
				count := 2
				if mode == "book" {
					count = 1
				}
				if mode == "both" {
					count = 3
				}
				if len(result.Paths) != count {
					t.Fatal(result)
				}
				for i, path := range result.Paths {
					if !filepath.IsAbs(path) {
						t.Fatal(path)
					}
					whole := mode != "chapters" && i == 0
					expected := want
					ci := i
					if mode == "both" {
						ci--
					}
					if !whole {
						if ci == 0 {
							expected = want[:26460]
						} else {
							expected = want[26460:]
						}
					}
					exportAudioEqual(t, exportDecode(t, path), expected, format == "wav" || format == "flac")
					inspect := audiotest.Inspect(t, path)
					title := p.Metadata.Title
					if !whole {
						title = p.Chapters[ci].Title
						if inspect.Tags["album"] != p.Metadata.Title || inspect.Tags["track"] != fmt.Sprintf("%d/2", ci+1) {
							t.Fatal(inspect.Tags)
						}
					}
					if inspect.Tags["title"] != title || inspect.Tags["artist"] != "Author" {
						t.Fatal(inspect.Tags)
					}
					if format != "wav" && inspect.Tags["composer"] != "Narrator" {
						t.Fatal(inspect.Tags)
					}
					if format != "wav" && inspect.Tags["keywords"] != "project" {
						t.Fatal(inspect.Tags)
					}
					if whole && (format == "m4a" || format == "m4b") {
						if !reflect.DeepEqual(inspect.Chapters, []audiotest.InspectedChapter{{StartFrame: 0, EndFrame: 26460}, {StartFrame: 26460, EndFrame: 48510}}) {
							t.Fatal(inspect.Chapters)
						}
					} else if len(inspect.Chapters) != 0 {
						t.Fatal("unexpected markers", inspect.Chapters)
					}
					if !whole && format == "m4b" && filepath.Ext(path) != ".m4a" {
						t.Fatal(path)
					}
					// Independent encode of the expected PCM interval must be bit-identical after decoding.
					if !whole && (format == "m4a" || format == "m4b") {
						pcm := filepath.Join(t.TempDir(), "reference.wav")
						if e := audio.WriteRF64(context.Background(), pcm, 1, func(emit func([]float32) error) error { return emit(expected) }); e != nil {
							t.Fatal(e)
						}
						ref := filepath.Join(t.TempDir(), "reference.m4a")
						if e := audio.EncodePCM(context.Background(), pcm, ref, audio.EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1}); e != nil {
							t.Fatal(e)
						}
						if !reflect.DeepEqual(exportDecode(t, path), exportDecode(t, ref)) {
							t.Fatal("chapter encoded from different PCM")
						}
					}
				}
				if mode != "book" {
					entries, e := os.ReadDir(dst)
					if e != nil {
						t.Fatal(e)
					}
					if len(entries) != 2 {
						t.Fatal("staging leaked", entries)
					}
				}
			})
		}
	}
}

func TestExportSnapshot(t *testing.T) {
	p, want := exportFixture(t)
	p.Export.Format = "flac"
	cloned := make(chan struct{})
	changed := make(chan struct{})
	go func() {
		<-cloned
		p.Chapters[0].Segments[0].StartUS = 0
		p.Chapters[0].Title = "Changed"
		p.Metadata.Extra["keywords"] = "changed"
		close(changed)
	}()
	first := true
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "book.flac"), nil, func(_, _ int, _ string) {
		if first {
			first = false
			close(cloned)
			<-changed
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	exportAudioEqual(t, exportDecode(t, result.Paths[0]), want, true)
	if audiotest.Inspect(t, result.Paths[0]).Tags["keywords"] != "project" {
		t.Fatal("snapshot changed")
	}
}

func assertNoStage(t *testing.T, parent string) {
	t.Helper()
	entries, e := os.ReadDir(parent)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".book-export-") {
			t.Fatal("staging leaked", entry.Name())
		}
	}
}
func TestExportCollisionRace(t *testing.T) {
	for _, mode := range []string{"book", "both"} {
		t.Run(mode, func(t *testing.T) {
			p, _ := exportFixture(t)
			p.Export.Format = "wav"
			p.Export.OutputMode = mode
			parent := t.TempDir()
			dst := filepath.Join(parent, "result")
			sentinel := []byte("existing destination wins")
			deps := defaultExportDeps()
			publish := deps.publish
			deps.publish = func(a, b string) error {
				if mode == "both" {
					if e := os.Mkdir(b, 0700); e != nil {
						return e
					}
					if e := os.WriteFile(filepath.Join(b, "keep"), sentinel, 0600); e != nil {
						return e
					}
				} else if e := os.WriteFile(b, sentinel, 0600); e != nil {
					return e
				}
				return publish(a, b)
			}
			if _, e := exportWith(context.Background(), p, dst, nil, nil, deps); e == nil {
				t.Fatal("collision accepted")
			}
			path := dst
			if mode == "both" {
				path = filepath.Join(dst, "keep")
			}
			got, e := os.ReadFile(path)
			if e != nil || !bytes.Equal(got, sentinel) {
				t.Fatal("destination changed", string(got), e)
			}
			assertNoStage(t, parent)
		})
	}
}
func TestExportPreflightCollisions(t *testing.T) {
	for _, kind := range []string{"existing", "dangling", "hardlink", "case"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := exportFixture(t)
			parent := t.TempDir()
			dst := filepath.Join(parent, "result.m4b")
			existing := dst
			switch kind {
			case "existing":
				if e := os.WriteFile(dst, []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			case "dangling":
				if e := os.Symlink(filepath.Join(parent, "missing"), dst); e != nil {
					t.Skipf("symlink unsupported: %v", e)
				}
			case "hardlink":
				if e := os.Link(p.Sources[0].Path, dst); e != nil {
					t.Fatal(e)
				}
			case "case":
				existing = filepath.Join(parent, "RESULT.M4B")
				if e := os.WriteFile(existing, []byte("keep"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			before, _ := os.ReadFile(existing)
			deps := defaultExportDeps()
			deps.render = func(context.Context, book.BookProject, []string, string, string) error {
				t.Error("decoded before collision check")
				return errors.New("should not render")
			}
			if _, e := exportWith(context.Background(), p, dst, nil, nil, deps); e == nil {
				t.Fatal("collision accepted")
			}
			after, _ := os.ReadFile(existing)
			if !bytes.Equal(before, after) {
				t.Fatal("existing changed")
			}
			if _, e := os.Lstat(existing); e != nil {
				t.Fatal(e)
			}
			assertNoStage(t, parent)
		})
	}
}
func TestExportCallback(t *testing.T) {
	p, _ := exportFixture(t)
	p.Export.Normalize = true
	p.Export.OutputMode = "both"
	p.Export.Format = "wav"
	last, total := -1, 0
	var active atomic.Bool
	_, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "result"), nil, func(n, max int, _ string) {
		if active.Swap(true) {
			t.Error("concurrent callback")
		}
		defer active.Store(false)
		if n < last || n > max || max <= 0 {
			t.Errorf("progress %d/%d after %d", n, max, last)
		}
		if total != 0 && total != max {
			t.Error("total changed")
		}
		last = n
		total = max
	})
	if e != nil {
		t.Fatal(e)
	}
	if last != total {
		t.Fatal("not complete", last, total)
	}
}
func TestExportPreserveFallback(t *testing.T) {
	p, _ := exportFixture(t)
	p.Export.Profile = "preserve"
	var messages []string
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "book.m4b"), nil, func(_, _ int, s string) { messages = append(messages, s) })
	if e != nil {
		t.Fatal(e)
	}
	reason := "preserve requires intact source audio in continuous original order"
	if !strings.Contains(result.NormalizationReport, reason) || !strings.Contains(strings.Join(messages, "\n"), reason) {
		t.Fatal(result, messages)
	}
}

// Catches per-chapter gain/retry, fourth attempts, and encoding retry from lossy output.
func TestExportNormalizationRetry(t *testing.T) {
	for _, neverPass := range []bool{false, true} {
		t.Run(fmt.Sprint(neverPass), func(t *testing.T) {
			p, _ := exportFixture(t)
			p.Export.Normalize = true
			p.Export.OutputMode = "both"
			p.Export.Format = "wav"
			parent := t.TempDir()
			dst := filepath.Join(parent, "result")
			deps := defaultExportDeps()
			realEncode := deps.encode
			realMeasure := deps.measure
			var gains []float64
			var pcmPath string
			var pcmBytes []byte
			deps.encode = func(ctx context.Context, in, out string, opts audio.EncodeOptions) error {
				b, e := os.ReadFile(in)
				if e != nil {
					return e
				}
				if pcmPath == "" {
					pcmPath = in
					pcmBytes = b
				} else if in != pcmPath || !bytes.Equal(b, pcmBytes) {
					t.Error("retry changed original PCM")
				}
				gains = append(gains, opts.GainDB)
				return realEncode(ctx, in, out, opts)
			}
			measurements := 0
			deps.measure = func(ctx context.Context, paths []string, dual bool) (audio.Loudness, error) {
				m, e := realMeasure(ctx, paths, dual)
				if e != nil {
					return m, e
				}
				measurements++
				if measurements == 1 {
					return audio.Loudness{IntegratedLUFS: -18, TruePeakDBTP: -4, HasIntegrated: true}, nil
				}
				m.TruePeakDBTP = -2.2
				if len(gains) <= 3 || neverPass {
					m.TruePeakDBTP = -1.5
				}
				return m, nil
			}
			result, e := exportWith(context.Background(), p, dst, nil, nil, deps)
			if neverPass {
				if e == nil {
					t.Fatal("overshoot published", result)
				}
				if _, e := os.Lstat(dst); !os.IsNotExist(e) {
					t.Fatal("target exists", e)
				}
				if len(gains) != 9 {
					t.Fatal(gains)
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				if len(gains) != 6 {
					t.Fatal(gains)
				}
				for i, g := range gains {
					want := 0.0
					if i >= 3 {
						want = -.6
					}
					if math.Abs(g-want) > 1e-8 {
						t.Fatal(gains)
					}
				}
				if !strings.Contains(result.NormalizationReport, "-2.20 dBTP") {
					t.Fatal(result)
				}
			}
			assertNoStage(t, parent)
		})
	}
}

func TestExportRollback(t *testing.T) {
	for _, phase := range []string{"cancel", "encode-enospc", "metadata", "trailer", "close", "verification", "sync", "rename"} {
		t.Run(phase, func(t *testing.T) {
			p, _ := exportFixture(t)
			p.Export.Format = "wav"
			parent := t.TempDir()
			dst := filepath.Join(parent, "out.wav")
			before, e := os.ReadFile(p.Sources[0].Path)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deps := defaultExportDeps()
			sentinel := errors.New(phase + " failure")
			switch phase {
			case "cancel":
				render := deps.render
				deps.render = func(ctx context.Context, p book.BookProject, ids []string, path, ch string) error {
					e := render(ctx, p, ids, path, ch)
					cancel()
					return e
				}
			case "metadata":
				p.Metadata.Title = "bad\x00tag"
			case "encode-enospc", "trailer", "close":
				encode := deps.encode
				deps.encode = func(ctx context.Context, in, out string, opts audio.EncodeOptions) error {
					if e := encode(ctx, in, out, opts); e != nil {
						return e
					}
					return sentinel
				}
			case "verification":
				deps.verify = func(context.Context, string, int, int64) error { return sentinel }
			case "sync":
				deps.sync = func(string) error { return sentinel }
			case "rename":
				deps.publish = func(string, string) error { return sentinel }
			}
			if _, e := exportWith(ctx, p, dst, nil, nil, deps); e == nil {
				t.Fatal("failure published")
			} else if phase != "metadata" && phase != "cancel" && !errors.Is(e, sentinel) {
				t.Fatal("error lost", e)
			}
			if _, e := os.Lstat(dst); !os.IsNotExist(e) {
				t.Fatal("target exists", e)
			}
			assertNoStage(t, parent)
			after, _ := os.ReadFile(p.Sources[0].Path)
			if !bytes.Equal(before, after) {
				t.Fatal("source changed")
			}
		})
	}
}
func TestExportCancelAfterCommit(t *testing.T) {
	p, _ := exportFixture(t)
	p.Export.Format = "wav"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := defaultExportDeps()
	publish := deps.publish
	deps.publish = func(a, b string) error { e := publish(a, b); cancel(); return e }
	result, e := exportWith(ctx, p, filepath.Join(t.TempDir(), "out.wav"), nil, nil, deps)
	if e != nil || len(result.Paths) != 1 {
		t.Fatal(result, e)
	}
	if _, e := os.Stat(result.Paths[0]); e != nil {
		t.Fatal(e)
	}
}

func TestExportSingleGainAcrossChapters(t *testing.T) {
	p, _ := exportFixture(t)
	p.Export.Normalize = true
	p.Export.OutputMode = "chapters"
	p.Export.Format = "wav"
	// Deliberately different amplitudes; independent chapter normalization would erase the 4:1 ratio.
	s := make([]int16, 44100)
	for i := range s {
		a := 12000.0
		if i >= 22050 {
			a = 3000
		}
		s[i] = int16(a * math.Sin(2*math.Pi*440*float64(i)/44100))
	}
	audiotest.WriteWAV(t, p.Sources[0].Path, 44100, 1, s)
	st, _ := os.Stat(p.Sources[0].Path)
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "out"), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	rms := func(s []float32) float64 {
		var sum float64
		for _, v := range s {
			sum += float64(v * v)
		}
		return math.Sqrt(sum / float64(len(s)))
	}
	a := exportDecode(t, result.Paths[0])
	b := exportDecode(t, result.Paths[1])
	ratio := rms(b) / rms(a[:22050])
	if math.Abs(ratio-4) > .01 {
		t.Fatal("relative levels changed", ratio)
	}
	measured, e := audio.MeasureR128(context.Background(), result.Paths, true)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(result.NormalizationReport, fmt.Sprintf("%.2f LUFS", measured.IntegratedLUFS)) {
		t.Fatal("report not whole programme", result, measured)
	}
	for _, path := range result.Paths {
		m, e := audio.MeasureR128(context.Background(), []string{path}, true)
		if e != nil || m.TruePeakDBTP > -1.99 {
			t.Fatal(m, e)
		}
	}
}

func exportCover(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cover.png")
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	im.Set(1, 1, color.RGBA{R: 255, A: 255})
	if e = png.Encode(f, im); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return path
}
func TestExportOwnedMetadataAndArtwork(t *testing.T) {
	p, _ := exportFixture(t)
	p.CoverPath = exportCover(t)
	// Source tags must never fill consciously cleared project fields.
	tagged := filepath.Join(t.TempDir(), "source.flac")
	if e := audio.EncodePCM(context.Background(), p.Sources[0].Path, tagged, audio.EncodeOptions{Format: "flac", Channels: 1, EndFrame: -1, Metadata: meta.AudiobookMetadata{Title: "Source title", Artist: "Source author", Composer: "Source narrator", Extra: map[string]string{"keywords": "source"}}}); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(tagged)
	p.Sources[0].Path = tagged
	p.Sources[0].SizeBytes = st.Size()
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Metadata.Artist = ""
	p.Metadata.Composer = ""
	p.Metadata.Subtitle = "Project subtitle"
	p.Metadata.Extra["private_storage_key"] = "kept in project"
	p.Metadata.Extra["artist"] = "wrong"
	p.Metadata.Extra["composer"] = "wrong"
	for _, format := range []string{"m4b", "flac", "wav"} {
		t.Run(format, func(t *testing.T) {
			p.Export.Format = format
			p.Export.OutputMode = "both"
			result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "export"), nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			for _, path := range result.Paths {
				got := audiotest.Inspect(t, path)
				if got.Tags["artist"] != "" || got.Tags["composer"] != "" {
					t.Fatal("explicit blank filled", got.Tags)
				}
				if format == "wav" {
					if got.Covers != 0 {
						t.Fatal(got)
					}
				} else if got.Covers != 1 {
					t.Fatal("missing artwork", got)
				}
				if format != "wav" && got.Tags["keywords"] != "project" {
					t.Fatal("extra lost with art", got.Tags)
				}
				if format == "flac" {
					if got.Tags["private_storage_key"] != "kept in project" {
						t.Fatal(got.Tags)
					}
				} else if !strings.Contains(result.NormalizationReport, filepath.Base(path)+": "+strings.TrimPrefix(filepath.Ext(path), ".")+" does not support Extra tag \"private_storage_key\"") {
					t.Fatal("missing per-output warning", result)
				}
			}
			if format == "wav" && !strings.Contains(result.NormalizationReport, "WAV does not support embedded cover artwork") {
				t.Fatal(result)
			}
			if format == "m4b" {
				if !strings.Contains(result.NormalizationReport, "does not support tag \"subtitle\"") {
					t.Fatal(result)
				}
				markers, e := chapter.ReadEmbeddedMarkers(result.Paths[0])
				if e != nil {
					t.Fatal(e)
				}
				if len(markers) != 2 {
					t.Fatal("chapter table", markers)
				}
				for i, marker := range markers {
					if marker.Title != p.Chapters[i].Title {
						t.Fatal("chapter title changed", markers)
					}
				}
			}
			if p.Metadata.Extra["private_storage_key"] != "kept in project" {
				t.Fatal("metadata mutated")
			}
		})
	}
}

func TestExportPreserveCompatibleAAC(t *testing.T) {
	p, _ := exportFixture(t)
	source := filepath.Join(t.TempDir(), "source.m4a")
	if e := audio.EncodePCM(context.Background(), p.Sources[0].Path, source, audio.EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: 44100, Metadata: meta.AudiobookMetadata{Artist: "source artist"}}); e != nil {
		t.Fatal(e)
	}
	st, _ := os.Stat(source)
	p.Sources[0].Path = source
	p.Sources[0].SizeBytes = st.Size()
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Export.Profile = "preserve"
	p.Metadata.Artist = ""
	p.Chapters = []book.Chapter{{ID: "a", Title: "Opening", Segments: []book.Segment{{SourceID: "s", EndUS: 500000}}}, {ID: "b", Title: "Ending", Segments: []book.Segment{{SourceID: "s", StartUS: 500000, EndUS: 1000000}}}}
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "out.m4b"), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(result.NormalizationReport, "Preserving compatible AAC stream") {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(exportDecode(t, source), exportDecode(t, result.Paths[0])) {
		t.Fatal("preserve reencoded audio")
	}
	inspect := audiotest.Inspect(t, result.Paths[0])
	if inspect.Tags["artist"] != "" || len(inspect.Chapters) != 2 {
		t.Fatal(inspect)
	}
}

func TestExportRealAACPeak(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	samples := make([]int16, 44100)
	// Short, full-scale transients amid quieter programme exercise lossy peak overshoot.
	for i := range samples {
		a := 2000.0
		if i%4410 < 40 {
			a = 30000
		}
		samples[i] = int16(a * math.Sin(2*math.Pi*997*float64(i)/44100))
	}
	audiotest.WriteWAV(t, p.Sources[0].Path, 44100, 1, samples)
	st, _ := os.Stat(p.Sources[0].Path)
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Export.Normalize = true
	p.Export.OutputMode = "both"
	p.Export.Profile = "economy"
	p.Chapters = []book.Chapter{{ID: "a", Title: "A", Segments: []book.Segment{{SourceID: "s", EndUS: 500000}}}, {ID: "b", Title: "B", Segments: []book.Segment{{SourceID: "s", StartUS: 500000, EndUS: 1000000}}}}
	retries := 0
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "export"), nil, func(_, _ int, message string) {
		if strings.Contains(message, "reducing every output") {
			retries++
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("real AAC overshoot retries: %d; %s", retries, result.NormalizationReport)
	if retries == 0 {
		t.Fatal("fixture did not exercise real AAC overshoot")
	}
	for _, path := range result.Paths {
		measured, e := audio.MeasureR128(context.Background(), []string{path}, true)
		if e != nil || measured.TruePeakDBTP > -1.99 {
			t.Fatal(measured, e)
		}
	}
}

func TestExportUndefinedNormalization(t *testing.T) {
	for _, kind := range []string{"silent", "short", "gated"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := fixtureProject(t, 44100)
			p.Export.Normalize = true
			p.Export.Format = "wav"
			samples := make([]int16, 44100)
			if kind == "short" {
				p.Chapters[0].Segments[0].EndUS = 100000
				for i := range samples {
					samples[i] = 30000
				}
			}
			if kind == "gated" {
				for i := range samples {
					samples[i] = 1
				}
			}
			audiotest.WriteWAV(t, p.Sources[0].Path, 44100, 1, samples)
			st, _ := os.Stat(p.Sources[0].Path)
			p.Sources[0].MTimeNS = st.ModTime().UnixNano()
			result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "out.wav"), nil, nil)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(result.NormalizationReport, "undefined LUFS") {
				t.Fatal(result)
			}
			decoded := exportDecode(t, result.Paths[0])
			var peak float64
			for _, v := range decoded {
				peak = math.Max(peak, math.Abs(float64(v)))
			}
			if kind == "silent" && peak != 0 || kind == "gated" && peak > 1.0/32768 || kind == "short" && peak > math.Pow(10, -1.99/20) {
				t.Fatal("undefined target amplified or violated ceiling", peak)
			}
		})
	}
}

func TestExportRejectsInvalidCover(t *testing.T) {
	for _, format := range []string{"m4b", "flac", "wav"} {
		p, _ := exportFixture(t)
		p.Export.Format = format
		p.CoverPath = filepath.Join(t.TempDir(), "invalid.png")
		if e := os.WriteFile(p.CoverPath, []byte("not image"), 0600); e != nil {
			t.Fatal(e)
		}
		parent := t.TempDir()
		dst := filepath.Join(parent, "out."+format)
		if _, e := Export(context.Background(), p, dst, nil, nil); e == nil {
			t.Fatal("invalid cover accepted", format)
		}
		if _, e := os.Lstat(dst); !os.IsNotExist(e) {
			t.Fatal(e)
		}
		assertNoStage(t, parent)
	}
}

func TestExportPublishNoReplace(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			parent := t.TempDir()
			src := filepath.Join(parent, "staged")
			dst := filepath.Join(parent, "final")
			if directory {
				if e := os.Mkdir(src, 0700); e != nil {
					t.Fatal(e)
				}
			} else if e := os.WriteFile(src, []byte("new"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(dst, []byte("original"), 0600); e != nil {
				t.Fatal(e)
			}
			if e := publishNoReplace(src, dst); e == nil {
				t.Fatal("overwrote destination")
			}
			if got, e := os.ReadFile(dst); e != nil || string(got) != "original" {
				t.Fatal(string(got), e)
			}
			if _, e := os.Lstat(src); e != nil {
				t.Fatal("staged input lost", e)
			}
			if e := os.Remove(dst); e != nil {
				t.Fatal(e)
			}
			if e := publishNoReplace(src, dst); e != nil {
				t.Fatal(e)
			}
			if _, e := os.Lstat(src); !os.IsNotExist(e) {
				t.Fatal("rename not committed", e)
			}
			if _, e := os.Lstat(dst); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestExportPublishExistingEmptyDirectory(t *testing.T) {
	parent := t.TempDir()
	src := filepath.Join(parent, "stage")
	dst := filepath.Join(parent, "result")
	for _, p := range []string{src, dst} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	before, e := os.Lstat(dst)
	if e != nil {
		t.Fatal(e)
	}
	if e = publishNoReplace(src, dst); e == nil {
		t.Fatal("replaced existing empty directory")
	}
	after, e := os.Lstat(dst)
	if e != nil || !os.SameFile(before, after) {
		t.Fatal("destination inode changed", e)
	}
	if _, e = os.Lstat(src); e != nil {
		t.Fatal("source moved", e)
	}
}

func TestExportCancellationBeforePublication(t *testing.T) {
	p, _ := exportFixture(t)
	p.Export.Format = "wav"
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := defaultExportDeps()
	syncFile := deps.sync
	deps.sync = func(path string) error { e := syncFile(path); cancel(); return e }
	deps.publish = func(string, string) error { t.Error("published after cancellation"); return nil }
	_, e := exportWith(ctx, p, filepath.Join(parent, "out.wav"), nil, nil, deps)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	assertNoStage(t, parent)
	entries, e := os.ReadDir(parent)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
}

func TestExportStagingOwnership(t *testing.T) {
	parent := t.TempDir()
	stage := filepath.Join(parent, "stage")
	if e := os.Mkdir(stage, 0700); e != nil {
		t.Fatal(e)
	}
	owned, e := os.Lstat(stage)
	if e != nil {
		t.Fatal(e)
	}
	os.SameFile(owned, owned) // as Export does: fix the identity before the path is reused
	if e = os.Rename(stage, filepath.Join(parent, "original-stage")); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(stage, 0700); e != nil {
		t.Fatal(e)
	}
	foreign := filepath.Join(stage, "foreign")
	if e = os.WriteFile(foreign, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = removeOwnedStage(stage, owned); e == nil {
		t.Fatal("unowned stage accepted")
	}
	if got, e := os.ReadFile(foreign); e != nil || string(got) != "keep" {
		t.Fatal(string(got), e)
	}
}

func TestExportReevaluatesGating(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	samples := make([]int16, 44100*4)
	for i := range samples {
		a := 24.0
		if i >= 44100*2 {
			a = 12
		}
		samples[i] = int16(a * math.Sin(2*math.Pi*997*float64(i)/44100))
	}
	audiotest.WriteWAV(t, p.Sources[0].Path, 44100, 1, samples)
	st, _ := os.Stat(p.Sources[0].Path)
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Sources[0].SizeBytes = st.Size()
	p.Sources[0].DurationUS = 4000000
	p.Chapters[0].Segments[0].EndUS = 4000000
	p.Export.Format = "wav"
	p.Export.Normalize = true
	input, e := audio.MeasureR128(context.Background(), []string{p.Sources[0].Path}, true)
	if e != nil {
		t.Fatal(e)
	}
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "out.wav"), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	final, e := audio.MeasureR128(context.Background(), result.Paths, true)
	if e != nil {
		t.Fatal(e)
	}
	if !input.HasIntegrated || !final.HasIntegrated || math.Abs(final.IntegratedLUFS+18) < .5 {
		t.Fatal("fixture must change gating", input, final)
	}
	if !strings.Contains(result.NormalizationReport, fmt.Sprintf("Final decoded whole book: %.2f LUFS", final.IntegratedLUFS)) {
		t.Fatal("reported estimated rather than remeasured LUFS", result, input, final)
	}
}

func TestExportCopyCompatibilityFallback(t *testing.T) {
	for _, fault := range []string{"compatibility", "joined-io", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			p, _ := fixtureProject(t, 44100)
			src := filepath.Join(t.TempDir(), "source.m4a")
			audiotest.WriteAAC(t, src, 44100, 0)
			st, _ := os.Stat(src)
			p.Sources[0].Path = src
			p.Sources[0].SizeBytes = st.Size()
			p.Sources[0].MTimeNS = st.ModTime().UnixNano()
			p.Export.Profile = "preserve"
			// Obtain an actual incompatibility from the copy boundary, rather than match strings.
			incompat := audio.CopyAAC(context.Background(), src, filepath.Join(t.TempDir(), "trim.m4a"), audio.EncodeOptions{Format: "m4a", Channels: 1, EndFrame: 10})
			if incompat == nil {
				t.Fatal("trim accepted")
			}
			deps := defaultExportDeps()
			deps.copy = func(ctx context.Context, in, out string, opts audio.EncodeOptions) error {
				switch fault {
				case "joined-io":
					return errors.Join(incompat, os.ErrPermission)
				case "cancel":
					return context.Canceled
				}
				return incompat
			}
			encode := deps.encode
			encodes := 0
			deps.encode = func(ctx context.Context, in, out string, opts audio.EncodeOptions) error {
				encodes++
				return encode(ctx, in, out, opts)
			}
			parent := t.TempDir()
			dst := filepath.Join(parent, "out.m4b")
			var messages []string
			result, e := exportWith(context.Background(), p, dst, nil, func(_, _ int, s string) { messages = append(messages, s) }, deps)
			if fault == "compatibility" {
				if e != nil {
					t.Fatal(e)
				}
				if encodes != 1 || !strings.Contains(result.NormalizationReport, incompat.Error()) || !strings.Contains(strings.Join(messages, "\n"), incompat.Error()) {
					t.Fatal("fallback not exposed", result, messages, encodes)
				}
				if strings.Contains(result.NormalizationReport, "Preserving compatible AAC stream") {
					t.Fatal("falsely claimed preservation", result)
				}
				if len(exportDecode(t, result.Paths[0])) != 44100 {
					t.Fatal("fallback duration")
				}
			} else {
				if e == nil || encodes != 0 {
					t.Fatal("operational failure recovered", e, encodes)
				}
				if _, e := os.Lstat(dst); !os.IsNotExist(e) {
					t.Fatal(e)
				}
			}
			assertNoStage(t, parent)
		})
	}
}

func TestExportRealPacketTimingFallback(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	src := filepath.Join(t.TempDir(), "source.m4a")
	audiotest.WriteAAC(t, src, 44100, 0)
	raw, e := os.ReadFile(src)
	if e != nil {
		t.Fatal(e)
	}
	// The sample table is one frame shorter than the declared edit duration.
	// RenderPCM permits the contract's single terminal frame fill; packet copy
	// must reject the mismatch even though its bounded header check succeeds.
	at := bytes.Index(raw, []byte("stts"))
	if at < 4 || at+12 > len(raw) {
		t.Fatal("missing sample time table")
	}
	n := int(binary.BigEndian.Uint32(raw[at+8:]))
	last := at + 12 + (n-1)*8 + 4
	if n < 1 || last+4 > len(raw) {
		t.Fatal("invalid stts")
	}
	duration := binary.BigEndian.Uint32(raw[last:])
	if duration < 2 {
		t.Fatal("tiny final packet")
	}
	binary.BigEndian.PutUint32(raw[last:], duration-1)
	if e = os.WriteFile(src, raw, 0600); e != nil {
		t.Fatal(e)
	}
	frames, e := audio.AACCopyFrames(context.Background(), src, 1)
	if e != nil || frames != 44100 {
		t.Fatal("fixture not header-compatible", frames, e)
	}
	if got := len(exportDecode(t, src)); got != 44099 {
		t.Fatal("fixture audible length", got)
	}
	st, _ := os.Stat(src)
	p.Sources[0].Path = src
	p.Sources[0].SizeBytes = st.Size()
	p.Sources[0].MTimeNS = st.ModTime().UnixNano()
	p.Export.Profile = "preserve"
	result, e := Export(context.Background(), p, filepath.Join(t.TempDir(), "out.m4b"), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(result.NormalizationReport, "Preserve fallback: AAC audible length 44099 differs from declared 44100") {
		t.Fatal(result)
	}
	if len(exportDecode(t, result.Paths[0])) != 44100 {
		t.Fatal("fallback changed book timeline")
	}
}

// Header inspection must obey the same pure-compatibility rule as packet copy.
// In particular, a transient joined close/I/O error must not disappear when a
// subsequent render could succeed.
func TestExportEarlyPreserveInspection(t *testing.T) {
	wav, _ := fixtureProject(t, 44100)
	_, incompatible := audio.AACCopyFrames(context.Background(), wav.Sources[0].Path, 1)
	if !audio.IsAACCopyIncompatible(incompatible) {
		t.Fatal("fixture must provide real typed incompatibility", incompatible)
	}
	for _, kind := range []string{"compatibility", "wrapped-compatibility", "io", "close", "source-change", "cancel", "cancel-after-success"} {
		t.Run(kind, func(t *testing.T) {
			p := preserveFixture(t)
			before, e := os.ReadFile(p.Sources[0].Path)
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			operational := error(nil)
			switch kind {
			case "io":
				operational = &os.PathError{Op: "read", Path: p.Sources[0].Path, Err: os.ErrPermission}
			case "close":
				operational = &os.PathError{Op: "close", Path: p.Sources[0].Path, Err: os.ErrPermission}
			case "source-change":
				changed := book.Clone(p)
				changed.Sources[0].SizeBytes++
				operational = book.CheckSources(changed)
			case "cancel", "cancel-after-success":
				operational = context.Canceled
			}
			deps := defaultExportDeps()
			inspected := false
			deps.inspectAAC = func(received context.Context, path string, channels int) (int64, error) {
				inspected = true
				if received != ctx || path != p.Sources[0].Path || channels != 1 {
					t.Error("inspection lost job context or source", received, path, channels)
				}
				switch kind {
				case "compatibility":
					return 0, incompatible
				case "wrapped-compatibility":
					return 0, fmt.Errorf("header: %w", errors.Join(incompatible))
				case "cancel-after-success":
					cancel()
					return 44100, nil
				default:
					return 0, errors.Join(incompatible, operational)
				}
			}
			rendered, published := false, false
			render := deps.render
			publish := deps.publish
			deps.render = func(ctx context.Context, p book.BookProject, ids []string, path, ch string) error {
				rendered = true
				return render(ctx, p, ids, path, ch)
			}
			deps.publish = func(a, b string) error { published = true; return publish(a, b) }
			parent := t.TempDir()
			dst := filepath.Join(parent, "out.m4b")
			var messages []string
			result, e := exportWith(ctx, p, dst, nil, func(_, _ int, s string) { messages = append(messages, s) }, deps)
			if !inspected {
				t.Fatal("inspection bypassed")
			}
			if operational == nil {
				if e != nil || !rendered || !published || !strings.Contains(result.NormalizationReport, incompatible.Error()) || !strings.Contains(strings.Join(messages, "\n"), incompatible.Error()) {
					t.Fatal("compatibility did not fall back", result, e, rendered, published)
				}
			} else {
				if e == nil || !errors.Is(e, operational) || rendered || published || len(result.Paths) != 0 {
					t.Fatal("operational inspection failure recovered", result, e, rendered, published)
				}
				if _, e := os.Lstat(dst); !os.IsNotExist(e) {
					t.Fatal("destination created", e)
				}
				if len(messages) != 0 {
					t.Fatal("failure reported as progress/fallback", messages)
				}
			}
			after, e := os.ReadFile(p.Sources[0].Path)
			if e != nil || !bytes.Equal(before, after) {
				t.Fatal("source changed", e)
			}
			assertNoStage(t, parent)
		})
	}
}
