package analysis

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func testSource(t *testing.T, id string, seconds int) Source {
	t.Helper()
	p := filepath.Join(t.TempDir(), id+".wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, seconds*44100))
	st, e := os.Stat(p)
	if e != nil {
		t.Fatal(e)
	}
	return Source{ID: id, Path: p, DurationUS: int64(seconds) * 1000000, SizeBytes: st.Size(), MTimeNS: st.ModTime().UnixNano()}
}
func TestAnalysisEditedTimeline(t *testing.T) {
	a, b := testSource(t, "a", 4), testSource(t, "b", 7)
	r, e := Analyze(context.Background(), Input{Sources: []Source{a, b}, Spans: []Span{{SourceID: "b", StartUS: 2000000, EndUS: 7000000}, {SilenceUS: 2000000}, {SourceID: "a", StartUS: 1000000, EndUS: 4000000}}, Channels: 1}, Options{CacheDir: t.TempDir()}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.DurationUS != 10000000 || r.ScannedUS != 10000000 {
		t.Fatalf("%+v", r)
	}
	if r.Counts["quiet"] != 1 || len(r.Findings) != 1 {
		t.Fatalf("%+v", r)
	}
	f := r.Findings[0]
	if *f.StartUS != 0 || *f.EndUS != 5000000 || len(f.Provenance) != 1 || f.Provenance[0].SourceID != "b" || f.Provenance[0].SourceStartUS != 2000000 || f.Provenance[0].SourceEndUS != 7000000 {
		t.Fatalf("%+v", f)
	}
	for _, c := range r.Coverage {
		if c.Detector == "repetition" && !c.Complete {
			t.Fatal("repeat coverage incomplete without a limit")
		}
	}
}
func TestAnalysisInputValidation(t *testing.T) {
	s := testSource(t, "a", 1)
	base := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}}, Channels: 1}
	cases := []Input{{Sources: []Source{s, s}, Spans: base.Spans, Channels: 1}, {Sources: base.Sources, Spans: base.Spans, Channels: 3}, {Sources: base.Sources, Spans: []Span{{SourceID: "missing", EndUS: 1}}, Channels: 1}, {Sources: base.Sources, Spans: []Span{{SourceID: "a", EndUS: 1}}, Channels: 1}, {Sources: base.Sources, Spans: []Span{{SilenceUS: math.MaxInt64}, {SilenceUS: 1}}, Channels: 1}, {Sources: base.Sources, Spans: []Span{{SourceID: "a", EndUS: 100, SilenceUS: 100}}, Channels: 1}}
	for i, in := range cases {
		if _, e := Analyze(context.Background(), in, Options{}, nil); e == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	deps := defaultDependencies()
	deps.inspect = func(ctx context.Context, p string, c int, s, e int64, emit func([]float32) error, problem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		err := emit([]float32{float32(math.NaN())})
		return audio.PCMInspection{}, err
	}
	if _, e := analyze(context.Background(), base, Options{}, nil, deps); e == nil {
		t.Fatal("nonfinite accepted")
	}
}
func TestAnalysisOutsideRangeFailure(t *testing.T) {
	s := testSource(t, "a", 3)
	deps := defaultDependencies()
	deps.inspect = func(ctx context.Context, p string, c int, s, e int64, emit func([]float32) error, problem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		problem(audio.PCMProblem{Kind: "decode_error", Detail: "damaged before range"})
		return audio.PCMInspection{}, nil
	}
	r, e := analyze(context.Background(), Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", StartUS: 1000000, EndUS: 3000000}}, Channels: 1}, Options{}, nil, deps)
	if e != nil || r.ScannedUS != 0 || len(r.Findings) != 1 || r.Findings[0].StartUS != nil {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestAnalysisCancellationAndLimits(t *testing.T) {
	s := testSource(t, "a", 2)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 2000000}}, Channels: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Analyze(ctx, in, Options{}, nil); e != context.Canceled {
		t.Fatal(e)
	}
	deps := defaultDependencies()
	deps.maxFrames = 44100
	r, e := analyze(context.Background(), in, Options{}, nil, deps)
	if e != nil || r.DurationUS != 2000000 || r.ScannedUS != 1000000 || len(r.Limitations) < 1 {
		t.Fatalf("%+v %v", r, e)
	}
	for _, c := range r.Coverage {
		if c.Complete {
			t.Fatalf("limit reported complete: %+v", c)
		}
	}
}

func TestAnalysisFindingLimitCoverage(t *testing.T) {
	s := testSource(t, "a", 30)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 30000000}}, Channels: 1}
	deps := defaultDependencies()
	deps.inspect = func(ctx context.Context, p string, c int, start, end int64, emit func([]float32) error, onProblem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		b := make([]float32, 4414)
		b[0] = 1
		b[1] = 1
		b[2] = 1
		var n int64
		for n < end-start {
			part := b[:min(int64(len(b)), end-start-n)]
			if e := emit(part); e != nil {
				return audio.PCMInspection{}, e
			}
			n += int64(len(part))
		}
		return audio.PCMInspection{EmittedFrames: n, Complete: true}, nil
	}
	r, e := analyze(context.Background(), in, Options{}, nil, deps)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Findings) != 200 || r.Counts["clipping"] <= 200 {
		t.Fatalf("%d %+v", len(r.Findings), r.Counts)
	}
	for _, c := range r.Coverage {
		if c.Detector == "clipping" && c.Complete {
			t.Fatal("display cap must mark affected detector incomplete")
		}
	}
}
func TestAnalysisImmutableSnapshot(t *testing.T) {
	s := testSource(t, "a", 1)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}}, Channels: 1}
	changed := false
	r, e := Analyze(context.Background(), in, Options{}, func(p Progress) {
		if !changed {
			in.Sources[0].Path = "removed"
			in.Spans[0].SourceID = "other"
			changed = true
		}
	})
	if e != nil || r.DurationUS != 1000000 || !changed {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestAnalysisSyntheticBoundaryAndCrossSource(t *testing.T) {
	a, b := testSource(t, "a", 3), testSource(t, "b", 3)
	for _, silence := range []bool{false, true} {
		spans := []Span{{SourceID: "a", EndUS: 3000000}}
		if silence {
			spans = append(spans, Span{SilenceUS: 100000})
		}
		spans = append(spans, Span{SourceID: "b", EndUS: 3000000})
		r, e := Analyze(context.Background(), Input{Sources: []Source{a, b}, Spans: spans, Channels: 1}, Options{}, nil)
		if e != nil {
			t.Fatal(e)
		}
		want := int64(1)
		if silence {
			want = 0
		}
		if r.Counts["quiet"] != want {
			t.Fatalf("silence %v: %+v", silence, r)
		}
		if !silence && len(r.Findings[0].Provenance) != 2 {
			t.Fatal(r.Findings)
		}
	}
}
func TestAnalysisDefaultDayLimit(t *testing.T) {
	r, e := Analyze(context.Background(), Input{Channels: 1, Spans: []Span{{SilenceUS: 86401000000}}}, Options{CacheDir: t.TempDir()}, nil)
	if e != nil || r.DurationUS != 86401000000 || r.ScannedUS != 86400000000 {
		t.Fatalf("%+v %v", r, e)
	}
	for _, c := range r.Coverage {
		if c.Complete {
			t.Fatal(c)
		}
	}
}
func TestAnalysisDecodeCancellation(t *testing.T) {
	s := testSource(t, "a", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, e := Analyze(ctx, Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 2000000}}, Channels: 1}, Options{CacheDir: t.TempDir()}, func(p Progress) {
		if p.Phase == "decode" {
			cancel()
		}
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestAnalysisDeduplicatesSourceObservations(t *testing.T) {
	s := testSource(t, "a", 1)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}, {SourceID: "a", EndUS: 1000000}}, Channels: 1}
	deps := defaultDependencies()
	deps.inspect = func(ctx context.Context, p string, c int, start, end int64, emit func([]float32) error, onProblem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		f := int64(441)
		pts := int64(300000)
		onProblem(audio.PCMProblem{Kind: "timestamp_discontinuity", SourceFrame: &f, OriginalTimestampUS: &pts, Detail: strings.Repeat("ż", 400)})
		b := make([]float32, 1024)
		for n := start; n < end; {
			part := b[:min(int64(len(b)), end-n)]
			if e := emit(part); e != nil {
				return audio.PCMInspection{}, e
			}
			n += int64(len(part))
		}
		return audio.PCMInspection{EmittedFrames: end - start, Complete: true}, nil
	}
	r, e := analyze(context.Background(), in, Options{}, nil, deps)
	if e != nil || len(r.Findings) != 1 || r.Counts["timestamp_discontinuity"] != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	f := r.Findings[0]
	if len(f.Provenance) != 2 || f.Provenance[0].OutputStartUS != 10000 || f.Provenance[1].OutputStartUS != 1010000 || len(f.Detail) > 512 || !utf8.ValidString(f.Detail) {
		t.Fatalf("%+v", f)
	}
}
func TestAnalysisPreservesFatalIssueAtFindingCap(t *testing.T) {
	s := testSource(t, "a", 1)
	deps := defaultDependencies()
	deps.inspect = func(ctx context.Context, p string, c int, start, end int64, emit func([]float32) error, onProblem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		for i := int64(0); i < 201; i++ {
			f := i
			onProblem(audio.PCMProblem{Kind: "timestamp_discontinuity", SourceFrame: &f, Detail: "timestamp"})
		}
		onProblem(audio.PCMProblem{Kind: "decode_error", Detail: "first fatal issue"})
		return audio.PCMInspection{}, nil
	}
	r, e := analyze(context.Background(), Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}}, Channels: 1}, Options{}, nil, deps)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Findings) != 200 || r.Findings[0].Kind != "decode_error" || r.Counts["timestamp_discontinuity"] != 201 {
		t.Fatalf("fatal issue missing: findings=%d first=%s counts=%v", len(r.Findings), r.Findings[0].Kind, r.Counts)
	}
}

func TestAnalysisSourceCountsBeyondDisplayCap(t *testing.T) {
	s := testSource(t, "a", 1)
	input := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}, {SourceID: "a", EndUS: 1000000}}, Channels: 1}
	deps := defaultDependencies()
	deps.inspect = sourceEventsInspector(201)
	report, err := analyze(context.Background(), input, Options{}, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 200 || report.Counts["timestamp_discontinuity"] != 201 {
		t.Fatalf("displayed=%d count=%d; want 200 displayed and 201 distinct source events", len(report.Findings), report.Counts["timestamp_discontinuity"])
	}
	for _, finding := range report.Findings {
		if len(finding.Provenance) != 2 {
			t.Fatalf("event %s lost its repeated output occurrence", finding.ID)
		}
	}
	for _, limitation := range report.Limitations {
		if strings.HasPrefix(limitation, "observation_dedup_limit:") {
			t.Fatal("display cap exhausted independent deduplication budget", limitation)
		}
	}
}

// The injected decoder emits real-sized PCM chunks after deterministic source
// events; the engine still owns mapping, counting, retention and both hashes.
func sourceEventsInspector(events int) func(context.Context, string, int, int64, int64, func([]float32) error, func(audio.PCMProblem)) (audio.PCMInspection, error) {
	return func(ctx context.Context, path string, channels int, start, end int64, emit func([]float32) error, onProblem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		for i := 0; i < events; i++ {
			frame := int64(i)
			onProblem(audio.PCMProblem{Kind: "timestamp_discontinuity", SourceFrame: &frame, Detail: "source clock jump"})
		}
		samples := make([]float32, 1024*channels)
		for frame := start; frame < end; {
			n := min(int64(len(samples)/channels), end-frame)
			if err := emit(samples[:int(n)*channels]); err != nil {
				return audio.PCMInspection{}, err
			}
			frame += n
		}
		return audio.PCMInspection{EmittedFrames: end - start, Complete: true}, nil
	}
}

func TestAnalysisSourceDedupBudget(t *testing.T) {
	s := testSource(t, "a", 1)
	span := Span{SourceID: "a", EndUS: 1000000}
	input := Input{Sources: []Source{s}, Spans: []Span{span, span, span}, Channels: 1}
	deps := defaultDependencies()
	deps.maxObservations = 2
	deps.inspect = sourceEventsInspector(3)
	report, err := analyze(context.Background(), input, Options{}, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	// Two remembered events deduplicate across all traversals; the third event
	// cannot enter the bounded set and contributes one occurrence per traversal.
	if report.Counts["timestamp_discontinuity"] != 5 || report.ScannedUS != 3000000 {
		t.Fatalf("count=%d scanned=%d", report.Counts["timestamp_discontinuity"], report.ScannedUS)
	}
	found := false
	for _, limitation := range report.Limitations {
		if strings.HasPrefix(limitation, "observation_dedup_limit:") && strings.Contains(limitation, "timestamp_discontinuity") && strings.Contains(limitation, "upper bound") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing precise counting limitation: %v", report.Limitations)
	}
	for _, coverage := range report.Coverage {
		if coverage.Detector == "timestamp_discontinuity" && coverage.Complete {
			t.Fatal("bounded deduplication reported exact complete coverage")
		}
		if coverage.Detector == "clipping" && !coverage.Complete {
			t.Fatal("source-event dedup budget incorrectly stopped scalar scan")
		}
	}
}

func TestAnalysisDefaultSourceDedupBudget(t *testing.T) {
	s := testSource(t, "a", 1)
	span := Span{SourceID: "a", EndUS: 1000000}
	deps := defaultDependencies()
	deps.inspect = sourceEventsInspector(16385)
	report, err := analyze(context.Background(), Input{Sources: []Source{s}, Spans: []Span{span, span}, Channels: 1}, Options{}, nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	// The fixed budget remembers 16384 events; one overflow event is counted
	// once per traversal. No event keys grow beyond the backend-owned budget.
	if report.Counts["timestamp_discontinuity"] != 16386 || len(report.Findings) != 200 {
		t.Fatalf("count=%d displayed=%d", report.Counts["timestamp_discontinuity"], len(report.Findings))
	}
	found := false
	for _, limitation := range report.Limitations {
		if strings.HasPrefix(limitation, "observation_dedup_limit:") {
			found = true
		}
	}
	if !found {
		t.Fatal("default deduplication budget was not enforced")
	}
}

func TestRepeatNativeProvenance(t *testing.T) {
	a := testSource(t, "repeat-a", 13)
	b := testSource(t, "repeat-b", 3)
	write := func(s *Source, seconds int, offset int64) {
		samples := make([]int16, seconds*44100)
		for i := range samples {
			samples[i] = int16(repeatSample(int64(i)+offset) * 32767)
		}
		audiotest.WriteWAV(t, s.Path, 44100, 1, samples)
		st, e := os.Stat(s.Path)
		if e != nil {
			t.Fatal(e)
		}
		s.SizeBytes = st.Size()
		s.MTimeNS = st.ModTime().UnixNano()
	}
	write(&a, 13, 0)
	write(&b, 3, 10000000)
	in := Input{Channels: 1, Sources: []Source{a, b}, Spans: []Span{{SourceID: a.ID, StartUS: 12345, EndUS: 6012345}, {SourceID: a.ID, StartUS: 6012345, EndUS: 12012345}, {SourceID: b.ID, EndUS: 3000000}, {SourceID: a.ID, StartUS: 12345, EndUS: 12012345}}}
	cache := t.TempDir()
	r, e := Analyze(context.Background(), in, Options{CacheDir: cache}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if r.Counts["repetition"] != 1 || r.RepeatScannedUS != 27000000 {
		t.Fatalf("%+v", r)
	}
	var f Finding
	for _, v := range r.Findings {
		if v.Kind == "repetition" {
			f = v
		}
	}
	if f.StartUS == nil || *f.StartUS != 0 || *f.EndUS != 12000000 || *f.SecondStartUS != 15000000 || *f.SecondEndUS != 27000000 || len(f.Provenance) != 3 {
		t.Fatalf("%+v", f)
	}
	if f.Provenance[0].SourceStartUS != 12345 || f.Provenance[1].SourceEndUS != 12012345 || f.Provenance[2].OutputStartUS != 15000000 {
		t.Fatal(f.Provenance)
	}
	es, _ := os.ReadDir(cache)
	if len(es) != 0 {
		t.Fatal("scratch leaked", es)
	}
}
func TestRepeatLimitScalarContinues(t *testing.T) {
	s := testSource(t, "continue", 15)
	deps := defaultDependencies()
	deps.repeatLimits.pcmBytes = 44100 * 2
	deps.inspect = func(ctx context.Context, p string, c int, start, end int64, emit func([]float32) error, problem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		b := make([]float32, 4096)
		var n int64
		for n < end-start {
			count := min(int64(len(b)), end-start-n)
			for i := range b[:count] {
				b[i] = 0
				if n+int64(i) < 9*44100 {
					b[i] = repeatSample(n + int64(i))
				}
				if n+int64(i) >= 8*44100 && n+int64(i) < 8*44100+4 {
					b[i] = 1
				}
			}
			if e := emit(b[:count]); e != nil {
				return audio.PCMInspection{}, e
			}
			n += count
		}
		return audio.PCMInspection{EmittedFrames: n, Complete: true}, nil
	}
	r, e := analyze(context.Background(), Input{Channels: 1, Sources: []Source{s}, Spans: []Span{{SourceID: s.ID, EndUS: 15000000}}}, Options{CacheDir: t.TempDir()}, nil, deps)
	if e != nil {
		t.Fatal(e)
	}
	if r.ScannedUS != 15000000 || r.RepeatScannedUS != 1000000 || r.Counts["clipping"] != 1 || r.Counts["quiet"] != 1 {
		t.Fatalf("%+v", r)
	}
	for _, c := range r.Coverage {
		if c.Detector == "repetition" && c.Complete {
			t.Fatal(c)
		}
		if c.Detector != "repetition" && !c.Complete {
			t.Fatal(c)
		}
	}
	if !strings.Contains(strings.Join(r.Limitations, " "), "repeat_pcm_limit") {
		t.Fatal(r.Limitations)
	}
}

func TestRepeatCombinedAnalysisMemory(t *testing.T) {
	// Exercise the existing full deduplication map and maximum timeline mapping
	// alongside SQLite and repeat buffers. Inputs are generated, never retained.
	var peaks []uint64
	for _, seconds := range []int64{30, 300} {
		src := testSource(t, "memory", 1)
		src.DurationUS = seconds * 1000000
		spans := make([]Span, 16384)
		spans[0] = Span{SourceID: src.ID, EndUS: src.DurationUS}
		for i := 1; i < len(spans); i++ {
			spans[i] = Span{SilenceUS: 250000}
		}
		deps := defaultDependencies()
		deps.repeatLimits.pcmBytes = 2 << 20
		deps.repeatLimits.indexBytes = 1 << 20
		deps.inspect = func(ctx context.Context, path string, channels int, start, end int64, emit func([]float32) error, onProblem func(audio.PCMProblem)) (audio.PCMInspection, error) {
			for i := 0; i < 16385; i++ {
				frame := int64(i)
				onProblem(audio.PCMProblem{Kind: "timestamp_discontinuity", SourceFrame: &frame, Detail: strings.Repeat("clock jump ", 80)})
			}
			b := make([]float32, 4096)
			for n := int64(0); n < end-start; {
				count := min(int64(len(b)), end-start-n)
				for i := range b[:count] {
					b[i] = repeatSample(n + int64(i))
				}
				if e := emit(b[:count]); e != nil {
					return audio.PCMInspection{}, e
				}
				n += count
			}
			return audio.PCMInspection{EmittedFrames: end - start, Complete: true}, nil
		}
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		var peak uint64
		var last int64
		report, e := analyze(context.Background(), Input{Sources: []Source{src}, Spans: spans, Channels: 1}, Options{CacheDir: t.TempDir()}, func(p Progress) {
			if p.Phase == "decode" && p.Current-last >= 10000000 {
				runtime.GC()
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				if current.HeapAlloc > before.HeapAlloc {
					peak = max(peak, current.HeapAlloc-before.HeapAlloc)
				}
				last = p.Current
			}
		}, deps)
		if e != nil {
			t.Fatal(e)
		}
		if peak > uint64(MaxGoBufferBytes) {
			t.Fatalf("combined live Go heap grew %d bytes", peak)
		}
		if len(report.Findings) > MaxFindings || report.Counts["timestamp_discontinuity"] != 16385 {
			t.Fatal(len(report.Findings), report.Counts)
		}
		for _, f := range report.Findings {
			if len(f.Detail) > MaxDetailBytes {
				t.Fatal("detail limit")
			}
		}
		peaks = append(peaks, peak)
		t.Logf("generated %ds: full engine+repeat observed live Go heap delta %d bytes (SQLite configured separately at %d bytes)", seconds, peak, MaxSQLiteCacheBytes)
	}
	if peaks[1] > peaks[0]+2<<20 {
		t.Fatalf("10x duration retained extra buffers: %v", peaks)
	}
}

func TestRepeatFindingPriorityAndCounts(t *testing.T) {
	src := testSource(t, "priority", 1)
	src.DurationUS = 27000000
	deps := defaultDependencies()
	deps.inspect = func(ctx context.Context, path string, c int, start, end int64, emit func([]float32) error, problem func(audio.PCMProblem)) (audio.PCMInspection, error) {
		for i := 0; i < 201; i++ {
			frame := int64(i)
			problem(audio.PCMProblem{Kind: "timestamp_discontinuity", SourceFrame: &frame, Detail: "clock jump"})
		}
		b := make([]float32, 4096)
		for n := int64(0); n < 27*44100; {
			count := min(int64(len(b)), 27*44100-n)
			for i := range b[:count] {
				f := n + int64(i)
				if f >= 15*44100 {
					f -= 15 * 44100
				}
				b[i] = repeatSample(f)
			}
			if e := emit(b[:count]); e != nil {
				return audio.PCMInspection{}, e
			}
			n += count
		}
		return audio.PCMInspection{EmittedFrames: 27 * 44100, Complete: true}, nil
	}
	r, e := analyze(context.Background(), Input{Channels: 1, Sources: []Source{src}, Spans: []Span{{SourceID: src.ID, EndUS: 27000000}}}, Options{CacheDir: t.TempDir()}, nil, deps)
	if e != nil {
		t.Fatal(e)
	}
	if r.Counts["repetition"] != 1 || r.Counts["timestamp_discontinuity"] != 201 || len(r.Findings) != 200 {
		t.Fatal(r.Counts, len(r.Findings))
	}
	for i, f := range r.Findings {
		if f.Kind != "timestamp_discontinuity" || f.ID != fmt.Sprintf("finding-%d", i+1) {
			t.Fatal(f)
		}
	}
	for _, c := range r.Coverage {
		if c.Detector == "repetition" && c.Complete {
			t.Fatal("omitted repeat reported complete")
		}
	}
}
