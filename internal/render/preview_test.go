package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
)

// Catches rendering A, ignoring B's edits, or retaining AAC priming/padding.
func TestPreviewChapterAudio(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	samples := make([]int16, 44100)
	for i := range samples {
		frequency := 440.0
		if i >= 22050 {
			frequency = 880
		}
		samples[i] = int16(10000 * math.Sin(2*math.Pi*frequency*float64(i)/44100))
	}
	audiotest.WriteWAV(t, p.Sources[0].Path, 44100, 1, samples)
	previewRefreshSource(t, &p.Sources[0])
	p.Chapters = append(p.Chapters, book.Chapter{ID: "b", Title: "B", Segments: []book.Segment{
		{SourceID: "s", StartUS: 600000, EndUS: 900000},
		{SilenceUS: 100000},
		{SourceID: "s", StartUS: 100000, EndUS: 300000},
	}})
	before, err := os.ReadFile(p.Sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	pcm := renderSamples(t, p, []string{"b"})
	want := append(pcmFloats(samples[26460:39690]), make([]float32, 4410)...)
	want = append(want, pcmFloats(samples[4410:13230])...)
	if !reflect.DeepEqual(pcm, want) {
		t.Fatal("selected PCM differs from edited intervals")
	}
	path, err := Preview(context.Background(), p, "b", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := previewDecoded(t, path, 1)
	if len(got) != 26460 || len(got) != len(pcm) {
		t.Fatalf("audible samples %d, want 26460", len(got))
	}
	reference := audiotest.ReferenceAAC(t, path)
	if len(reference) < len(got) || !reflect.DeepEqual(got, reference[:len(got)]) {
		t.Fatal("preview differs from independent AAC decoder")
	}
	// Compare signal at its actual sample offsets, without a guessed delay shift.
	for _, window := range [][2]int{{1024, 12200}, {18664, 25436}} {
		var delta, power float64
		for i := window[0]; i < window[1]; i++ {
			d := float64(got[i] - want[i])
			delta += d * d
			power += float64(want[i]) * float64(want[i])
		}
		if delta/power > .025 {
			t.Fatalf("AAC signal misaligned: error ratio %g", delta/power)
		}
	}
	for _, v := range got[14254:16616] {
		if math.Abs(float64(v)) > .003 {
			t.Fatal("inserted silence is not silent")
		}
	}
	if info := audiotest.Inspect(t, path); info.DurationFrames != 26460 {
		t.Fatalf("container duration %d", info.DurationFrames)
	}
	after, err := os.ReadFile(p.Sources[0].Path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source changed", err)
	}
}

// Catches whole-book rendering, export-dependent channels/profile/gain, and
// metadata/cover discovery. The nonselected source is not decodable audio.
func TestPreviewNoWholeBookWork(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	other := previewUndecodableSource(t)
	p.Sources = append(p.Sources, other)
	p.Chapters = append([]book.Chapter{{ID: "other", Title: "Other", Segments: []book.Segment{{SourceID: other.ID, EndUS: 1000000}}}}, p.Chapters...)
	p.Export = book.ExportSettings{Normalize: true, Profile: "preserve", Channels: "mono", Format: "flac", OutputMode: "both"}
	p.CoverPath = filepath.Join(t.TempDir(), "missing-cover.png")
	before := book.Clone(p)
	deps := defaultPreviewDeps()
	renders, encodes := 0, 0
	deps.render = func(ctx context.Context, snapshot book.BookProject, ids []string, path, channels string) error {
		renders++
		if !reflect.DeepEqual(ids, []string{"a"}) || channels != "auto" || snapshot.Export.Normalize || snapshot.Export.Profile != "standard" {
			t.Fatal("preview inherited export settings or selected the whole book", ids, channels, snapshot.Export)
		}
		// A callback can edit the caller without changing the already captured job.
		p.Chapters[1].Segments[0].EndUS = 100000
		p.Metadata.Extra = map[string]string{"edited": "caller"}
		return RenderPCM(ctx, snapshot, ids, path, channels)
	}
	deps.encode = func(ctx context.Context, input, output string, opts audio.EncodeOptions) error {
		encodes++
		if opts.Format != "m4a" || opts.Channels != 2 || opts.BitRate != 96000 || opts.GainDB != 0 || opts.StartFrame != 0 || opts.EndFrame != -1 || opts.CoverPath != "" || len(opts.Chapters) != 0 {
			t.Fatal("wrong preview encoding", opts)
		}
		return audio.EncodePCM(ctx, input, output, opts)
	}
	path, err := previewWith(context.Background(), p, "a", t.TempDir(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if renders != 1 || encodes != 1 {
		t.Fatal("extra render/encode pass", renders, encodes)
	}
	if got := previewDecoded(t, path, 2); len(got) != 88200 {
		t.Fatal("snapshot or auto channel selection lost", len(got))
	}
	// The job's override must never leak into caller settings.
	if p.Export != before.Export {
		t.Fatal("caller export settings mutated")
	}
}

// Catches ID-based stale reuse and collisions between concurrent edited previews.
func TestPreviewCacheIsolation(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	t.Chdir(t.TempDir())
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(workingDir, "cache")
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	relativeCache, err := filepath.Rel(workingDir, cache)
	if err != nil {
		t.Fatal(err)
	}
	projects := []book.BookProject{book.Clone(p), book.Clone(p)}
	projects[0].Chapters[0].Segments = []book.Segment{{SilenceUS: 200000}}
	projects[1].Chapters[0].Segments = []book.Segment{{SilenceUS: 350000}}
	paths := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range projects {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = Preview(context.Background(), projects[i], "a", relativeCache)
		}(i)
	}
	wg.Wait()
	for i, path := range paths {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !filepath.IsAbs(path) || filepath.Ext(path) != ".m4a" || filepath.Dir(filepath.Dir(path)) != cache {
			t.Fatal("nonprivate cache path", path)
		}
		info, err := os.Stat(filepath.Dir(path))
		// Windows reports 0777 for every directory; privacy comes from the profile ACL.
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0700) {
			t.Fatal("preview directory is not private", info, err)
		}
		want := []int{8820, 15435}[i]
		if got := previewDecoded(t, path, 1); len(got) != want {
			t.Fatal("stale preview", len(got), want)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
			t.Fatal("PCM survived success", entries, err)
		}
	}
	if paths[0] == paths[1] || filepath.Dir(paths[0]) == filepath.Dir(paths[1]) {
		t.Fatal("concurrent previews share output")
	}
	p.Sources[0].SizeBytes++
	if path, err := Preview(context.Background(), p, "a", cache); err == nil || path != "" {
		t.Fatal("same project ID skipped revalidation", path, err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("successful cache was removed", err)
		}
	}
}

// Catches private artifacts surviving failures and cleanup deleting sibling data.
func TestPreviewCleanup(t *testing.T) {
	for _, failure := range []string{"render", "encode", "cancel-render", "cancel-encode"} {
		t.Run(failure, func(t *testing.T) {
			p, _ := fixtureProject(t, 44100)
			cache := t.TempDir()
			sentinel := filepath.Join(cache, "existing.m4a")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("injected I/O failure")
			deps := defaultPreviewDeps()
			deps.render = func(ctx context.Context, p book.BookProject, ids []string, path, ch string) error {
				if err := os.WriteFile(path, []byte("partial PCM"), 0600); err != nil {
					return err
				}
				if failure == "render" {
					return injected
				}
				if failure == "cancel-render" {
					cancel()
				}
				return nil
			}
			deps.encode = func(ctx context.Context, input, output string, opts audio.EncodeOptions) error {
				if failure == "cancel-render" {
					t.Fatal("encode ran after cancellation")
				}
				if err := os.WriteFile(output, []byte("partial M4A"), 0600); err != nil {
					return err
				}
				if failure == "cancel-encode" {
					cancel()
					return nil
				}
				return injected
			}
			path, err := previewWith(ctx, p, "a", cache, deps)
			want := injected
			if failure == "cancel-render" || failure == "cancel-encode" {
				want = context.Canceled
			}
			if path != "" || !errors.Is(err, want) {
				t.Fatal("wrong failure result", path, err)
			}
			entries, err := os.ReadDir(cache)
			if err != nil || len(entries) != 1 || entries[0].Name() != "existing.m4a" {
				t.Fatal("owned cache not cleaned", entries, err)
			}
			if got, err := os.ReadFile(sentinel); err != nil || string(got) != "keep" {
				t.Fatal("sibling overwritten", err)
			}
		})
	}
}

// Catches missing/changed nonselected sources, invalid selection, and creating
// private output before validating or observing a canceled request.
func TestPreviewValidation(t *testing.T) {
	for _, failure := range []string{"missing", "changed", "unknown-id", "empty-id", "canceled", "invalid-project"} {
		t.Run(failure, func(t *testing.T) {
			p, _ := fixtureProject(t, 44100)
			other := previewUndecodableSource(t)
			p.Sources = append(p.Sources, other)
			cache := t.TempDir()
			id := "a"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "missing":
				if err := os.Remove(other.Path); err != nil {
					t.Fatal(err)
				}
			case "changed":
				p.Sources[1].MTimeNS++
			case "unknown-id":
				id = "missing"
			case "empty-id":
				id = ""
			case "canceled":
				cancel()
			case "invalid-project":
				p.SchemaVersion++
			}
			deps := defaultPreviewDeps()
			deps.render = func(context.Context, book.BookProject, []string, string, string) error {
				t.Fatal("render ran before validation")
				return nil
			}
			if path, err := previewWith(ctx, p, id, cache, deps); path != "" || err == nil {
				t.Fatal("invalid request accepted", path, err)
			}
			if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
				t.Fatal("validation created cache", entries, err)
			}
		})
	}
}

func previewRefreshSource(t *testing.T, src *book.Source) {
	t.Helper()
	info, err := os.Stat(src.Path)
	if err != nil {
		t.Fatal(err)
	}
	src.SizeBytes, src.MTimeNS = info.Size(), info.ModTime().UnixNano()
}

func previewUndecodableSource(t *testing.T) book.Source {
	t.Helper()
	src := book.Source{ID: "unused", Path: filepath.Join(t.TempDir(), "not-audio"), DurationUS: 1000000, SampleRate: 44100, Channels: 2}
	if err := os.WriteFile(src.Path, []byte("this is not audio"), 0600); err != nil {
		t.Fatal(err)
	}
	previewRefreshSource(t, &src)
	return src
}

func previewDecoded(t *testing.T, path string, channels int) []float32 {
	t.Helper()
	var samples []float32
	if err := audio.DecodePCM(context.Background(), path, channels, 0, -1, func(chunk []float32) error {
		samples = append(samples, chunk...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return samples
}
