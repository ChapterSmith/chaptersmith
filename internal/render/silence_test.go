package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var suggestDefaults = book.SilenceOptions{WindowUS: 5000000, MinSilenceUS: 500000, ThresholdDB: -40}

// Float PCM retains the threshold sample values without integer quantization.
func silenceProject(t *testing.T, seconds, channels int, boundaries []int64, sample func(frame int64, channel int) float32) book.BookProject {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.wav")
	err := audio.WriteRF64(context.Background(), path, channels, func(emit func([]float32) error) error {
		buf := make([]float32, 4096*channels)
		for pos := int64(0); pos < int64(seconds)*44100; {
			n := min(int64(4096), int64(seconds)*44100-pos)
			for i := int64(0); i < n; i++ {
				for ch := 0; ch < channels; ch++ {
					buf[int(i)*channels+ch] = sample(pos+i, ch)
				}
			}
			if err := emit(buf[:int(n)*channels]); err != nil {
				return err
			}
			pos += n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	p := book.BookProject{ID: "p", SchemaVersion: 1,
		Sources: []book.Source{{ID: "s", Path: path, DurationUS: int64(seconds) * 1000000, SizeBytes: st.Size(), MTimeNS: st.ModTime().UnixNano(), SampleRate: 44100, Channels: channels}},
		Export:  book.ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"}}
	start := int64(0)
	for i, end := range append(append([]int64{}, boundaries...), int64(seconds)*1000000) {
		p.Chapters = append(p.Chapters, book.Chapter{ID: string(rune('a' + i)), Title: "Chapter", Segments: []book.Segment{{SourceID: "s", StartUS: start, EndUS: end}}})
		start = end
	}
	return p
}

func silentRuns(ranges ...[2]int64) func(int64, int) float32 {
	return func(frame int64, _ int) float32 {
		for _, r := range ranges {
			if frame >= r[0] && frame < r[1] {
				return 0
			}
		}
		return .25
	}
}

func requireSuggestions(t *testing.T, p book.BookProject, opts book.SilenceOptions, want ...book.BoundaryProposal) {
	t.Helper()
	before := book.Clone(p)
	data, err := os.ReadFile(p.Sources[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	got, err := SuggestBoundaries(context.Background(), p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("proposals %v, want %v", got, want)
	}
	for i := range got {
		if got[i].ChapterID != want[i].ChapterID || got[i].OldStartUS != want[i].OldStartUS || math.Abs(float64(got[i].NewStartUS-want[i].NewStartUS)) > 23 {
			t.Fatalf("proposals %v, want %v (within one sample)", got, want)
		}
	}
	if !reflect.DeepEqual(p, before) {
		t.Fatal("project mutated")
	}
	data, err = os.ReadFile(p.Sources[0].Path)
	if err != nil || sha256.Sum256(data) != hash {
		t.Fatal("source changed", err)
	}
}

func TestSuggestNearestSilence(t *testing.T) {
	p := silenceProject(t, 16, 1, []int64{10000000}, silentRuns([2]int64{449820, 476280}))
	requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 10000000, NewStartUS: 10500000})
}

func TestSuggestThreshold(t *testing.T) {
	for _, tc := range []struct {
		name           string
		amplitude      float32
		frames         int64
		channels       int
		audibleChannel bool
		threshold      float64
		accept         bool
	}{
		{"at_minus40", .01, 22050, 1, false, -40, true},
		{"negative_at_minus40", -.01, 22050, 1, false, -40, true},
		{"negative_above_minus40", -.011, 22050, 1, false, -40, false},
		{"above_minus40", math.Nextafter32(.01, 1), 22050, 1, false, -40, false},
		{"one_frame_short", 0, 22049, 1, false, -40, false},
		{"stereo_audible_channel", 0, 22050, 2, true, -40, false},
		{"exact_float_threshold", .015625, 22050, 1, false, 20 * math.Log10(.015625), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := silenceProject(t, 3, tc.channels, []int64{1000000}, func(frame int64, ch int) float32 {
				if frame >= 48510 && frame < 48510+tc.frames && !(tc.audibleChannel && ch == 1) {
					return tc.amplitude
				}
				return .25
			})
			opts := suggestDefaults
			opts.ThresholdDB = tc.threshold
			if tc.accept {
				requireSuggestions(t, p, opts, book.BoundaryProposal{ChapterID: "b", OldStartUS: 1000000, NewStartUS: 1350000})
			} else {
				requireSuggestions(t, p, opts)
			}
		})
	}
}

func TestSuggestNoSilence(t *testing.T) {
	p := silenceProject(t, 3, 1, []int64{1000000}, silentRuns())
	requireSuggestions(t, p, suggestDefaults)
}

func TestSuggestTie(t *testing.T) {
	p := silenceProject(t, 5, 1, []int64{2000000}, silentRuns([2]int64{44100, 66150}, [2]int64{110250, 132300}))
	requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 2000000, NewStartUS: 1250000})
}

func TestSuggestTieBeforeMidpointRounding(t *testing.T) {
	// Both 22051-frame runs have half-frame midpoints exactly 22050.5 frames
	// from the boundary. Rounding first would make the later run appear closer.
	p := silenceProject(t, 5, 1, []int64{2000000}, silentRuns([2]int64{55124, 77175}, [2]int64{99225, 121276}))
	requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 2000000, NewStartUS: 1499977})
}

func TestSuggestSyntheticSilence(t *testing.T) {
	p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns([2]int64{30870, 57330}))
	p.Chapters[0].Segments = append(p.Chapters[0].Segments, book.Segment{SilenceUS: 600000})
	// Source silence on either side lasts .3s each; inserted .6s cannot join it.
	requireSuggestions(t, p, suggestDefaults)
	p = silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
	p.Chapters[0].Segments = append(p.Chapters[0].Segments, book.Segment{SilenceUS: 1000000})
	requireSuggestions(t, p, suggestDefaults)
}

func TestSuggestBoundaries(t *testing.T) {
	t.Run("first_start_and_noop", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns([2]int64{0, 22050}, [2]int64{33075, 55125}))
		requireSuggestions(t, p, suggestDefaults)
	})
	t.Run("crossing", func(t *testing.T) {
		p := silenceProject(t, 4, 1, []int64{1000000, 2000000}, silentRuns([2]int64{99225, 121275}))
		requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "c", OldStartUS: 2000000, NewStartUS: 2500000})
	})
	t.Run("chronological_conflict", func(t *testing.T) {
		p := silenceProject(t, 3, 1, []int64{1000000, 1200000}, silentRuns([2]int64{41895, 50715}))
		opts := book.SilenceOptions{WindowUS: 300000, MinSilenceUS: 100000, ThresholdDB: -40}
		requireSuggestions(t, p, opts, book.BoundaryProposal{ChapterID: "b", OldStartUS: 1000000, NewStartUS: 1050000})
	})
}

func TestSuggestPreservesAudio(t *testing.T) {
	runs := silentRuns([2]int64{48510, 74970}, [2]int64{114660, 141120})
	p := silenceProject(t, 5, 1, []int64{1000000, 3000000}, func(frame int64, ch int) float32 {
		if runs(frame, ch) != 0 {
			return .25 + float32(frame%53)/1000
		}
		if frame%2 == 0 {
			return math.Float32frombits(0x80000000)
		}
		return 0
	})
	renderBytes := func(project book.BookProject) []byte {
		path := filepath.Join(t.TempDir(), "render.wav")
		if err := RenderPCM(context.Background(), project, nil, path, "auto"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := renderBytes(p)
	proposals, err := SuggestBoundaries(context.Background(), p, suggestDefaults)
	if err != nil || len(proposals) != 2 {
		t.Fatal(proposals, err)
	}
	duration := book.DurationUS(p)
	for _, proposal := range proposals {
		p, err = book.ShiftBoundary(p, proposal.ChapterID, proposal.NewStartUS, "transfer")
		if err != nil {
			t.Fatal(err)
		}
	}
	if book.DurationUS(p) != duration || !bytes.Equal(before, renderBytes(p)) {
		t.Fatal("accepted transfers changed audio/duration")
	}
}

func TestSuggestWindowAndErrors(t *testing.T) {
	t.Run("clipped_edges", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{100000}, silentRuns([2]int64{0, 22050}))
		requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 100000, NewStartUS: 250000})
	})
	t.Run("clipped_right_edge", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1900000}, silentRuns([2]int64{66150, 88200}))
		requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 1900000, NewStartUS: 1750000})
	})
	t.Run("minimum_not_rounded_down", func(t *testing.T) {
		p := silenceProject(t, 3, 1, []int64{1000000}, silentRuns([2]int64{48510, 70560}))
		opts := suggestDefaults
		opts.MinSilenceUS = 500001
		requireSuggestions(t, p, opts)
	})
	t.Run("maximum_window", func(t *testing.T) {
		p := silenceProject(t, 3, 1, []int64{1000000}, silentRuns([2]int64{48510, 74970}))
		opts := suggestDefaults
		opts.WindowUS = math.MaxInt64
		requireSuggestions(t, p, opts, book.BoundaryProposal{ChapterID: "b", OldStartUS: 1000000, NewStartUS: 1400000})
	})
	t.Run("outside_window", func(t *testing.T) {
		p := silenceProject(t, 4, 1, []int64{1000000}, silentRuns([2]int64{88200, 114660}))
		opts := suggestDefaults
		opts.WindowUS = 500000
		requireSuggestions(t, p, opts)
	})
	t.Run("reordered_sources", func(t *testing.T) {
		p := silenceProject(t, 3, 1, nil, silentRuns([2]int64{13230, 39690}))
		p.Chapters = []book.Chapter{
			{ID: "a", Title: "A", Segments: []book.Segment{{SourceID: "s", StartUS: 1000000, EndUS: 2000000}, {SourceID: "s", StartUS: 300000, EndUS: 500000}}},
			{ID: "b", Title: "B", Segments: []book.Segment{{SourceID: "s", StartUS: 500000, EndUS: 1000000}}},
		}
		requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 1200000, NewStartUS: 1300000})
	})
	t.Run("multiple_sources_reordered", func(t *testing.T) {
		p := silenceProject(t, 2, 1, nil, silentRuns())
		other := silenceProject(t, 2, 1, nil, silentRuns([2]int64{13230, 39690}))
		other.Sources[0].ID = "other"
		p.Sources = append(p.Sources, other.Sources[0])
		p.Chapters = []book.Chapter{
			{ID: "a", Title: "A", Segments: []book.Segment{{SourceID: "s", StartUS: 1000000, EndUS: 2000000}, {SourceID: "other", StartUS: 300000, EndUS: 500000}}},
			{ID: "b", Title: "B", Segments: []book.Segment{{SourceID: "other", StartUS: 500000, EndUS: 1000000}, {SourceID: "s", EndUS: 1000000}}},
		}
		requireSuggestions(t, p, suggestDefaults, book.BoundaryProposal{ChapterID: "b", OldStartUS: 1200000, NewStartUS: 1300000})
	})
	t.Run("domain_mapping", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{500001}, silentRuns([2]int64{13230, 39690}))
		p.Chapters[0].Segments[0].StartUS = 100001
		got, err := SuggestBoundaries(context.Background(), p, suggestDefaults)
		want := []book.BoundaryProposal{{ChapterID: "b", OldStartUS: 400000, NewStartUS: 499999}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal(got, err, "want", want)
		}
	})
	t.Run("invalid_options", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
		for _, opts := range []book.SilenceOptions{{}, {WindowUS: -1, MinSilenceUS: 1}, {WindowUS: 1, MinSilenceUS: 0}, {WindowUS: 1, MinSilenceUS: 1, ThresholdDB: math.NaN()}, {WindowUS: 1, MinSilenceUS: 1, ThresholdDB: math.Inf(-1)}, {WindowUS: 1, MinSilenceUS: 1, ThresholdDB: .1}} {
			if _, err := SuggestBoundaries(context.Background(), p, opts); err == nil {
				t.Fatal("invalid options accepted", opts)
			}
		}
	})
	t.Run("full_source_check", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
		p.Sources = append(p.Sources, book.Source{ID: "unused", Path: filepath.Join(t.TempDir(), "missing.wav"), DurationUS: 1000000, SampleRate: 44100, Channels: 1})
		if _, err := SuggestBoundaries(context.Background(), p, suggestDefaults); err == nil {
			t.Fatal("unused missing source ignored")
		}
	})
	t.Run("changed_fingerprint", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
		p.Sources[0].SizeBytes++
		if _, err := SuggestBoundaries(context.Background(), p, suggestDefaults); err == nil {
			t.Fatal("changed source accepted")
		}
	})
	t.Run("corruption", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
		if err := os.WriteFile(p.Sources[0].Path, []byte("not audio"), 0600); err != nil {
			t.Fatal(err)
		}
		st, _ := os.Stat(p.Sources[0].Path)
		p.Sources[0].SizeBytes = st.Size()
		p.Sources[0].MTimeNS = st.ModTime().UnixNano()
		if _, err := SuggestBoundaries(context.Background(), p, suggestDefaults); err == nil {
			t.Fatal("corrupt audio accepted")
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := SuggestBoundaries(ctx, p, suggestDefaults); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("zero_grid_segment", func(t *testing.T) {
		p := silenceProject(t, 2, 1, []int64{1000000}, silentRuns())
		p.Chapters[0].Segments[0].EndUS = 1
		if _, err := SuggestBoundaries(context.Background(), p, suggestDefaults); err == nil {
			t.Fatal("zero-frame segment accepted")
		}
	})
}

func TestSuggestOnlyWindowReachesConsumer(t *testing.T) {
	p := silenceProject(t, 3, 1, []int64{1000000}, silentRuns())
	timeline, err := sourceTimeline(p)
	if err != nil {
		t.Fatal(err)
	}
	var frames int64
	err = streamSourceWindow(context.Background(), p, timeline, 1, 22050, 66150, func(pos int64, samples []float32) error {
		if pos != 22050+frames || len(samples) > 16384 {
			t.Fatal("outside window or unbounded chunk", pos, len(samples))
		}
		frames += int64(len(samples))
		return nil
	})
	if err != nil || frames != 44100 {
		t.Fatal(frames, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = streamSourceWindow(ctx, p, timeline, 1, 0, 132300, func(_ int64, _ []float32) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("active cancellation ignored", err)
	}
}

func TestSuggestBoundariesCanSearchOneChapter(t *testing.T) {
	// One second of silence centred half a second after each boundary.
	quiet := silentRuns([2]int64{10 * 44100, 11 * 44100}, [2]int64{20 * 44100, 21 * 44100})
	p := silenceProject(t, 30, 1, []int64{10e6, 20e6}, quiet)
	b := book.BoundaryProposal{ChapterID: "b", OldStartUS: 10000000, NewStartUS: 10500000}
	c := book.BoundaryProposal{ChapterID: "c", OldStartUS: 20000000, NewStartUS: 20500000}
	requireSuggestions(t, p, suggestDefaults, b, c)

	only := suggestDefaults
	only.ChapterID = "c"
	requireSuggestions(t, p, only, c)
	only.ChapterID = "a"
	requireSuggestions(t, p, only)
	only.ChapterID = "missing"
	if _, err := SuggestBoundaries(context.Background(), p, only); err == nil {
		t.Fatal("an unknown chapter was searched")
	}
}
