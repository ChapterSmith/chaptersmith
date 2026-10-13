package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Counter-based PCM is reproducible at any position, without retaining duration-sized input.
func repeatSample(frame int64) float32 {
	x := uint64(frame+1) * 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return float32(int32(x^(x>>31))%24000) / 32767
}
func feedRepeat(t *testing.T, s *repeatScanner, start, frames int64, sample func(int64) float32) {
	t.Helper()
	b := make([]float32, 4093*s.channels)
	for n := int64(0); n < frames; {
		count := min(int64(4093), frames-n)
		for i := int64(0); i < count; i++ {
			for c := 0; c < s.channels; c++ {
				b[int(i)*s.channels+c] = sample(n + i)
			}
		}
		if err := s.Push(b[:int(count)*s.channels], start+n); err != nil {
			t.Fatal(err)
		}
		n += count
	}
}
func newTestRepeat(t *testing.T, limits repeatLimits) *repeatScanner {
	t.Helper()
	s, e := newRepeatScanner(context.Background(), t.TempDir(), 1, limits)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return s
}
func TestRepeatVerifiedRanges(t *testing.T) {
	s := newTestRepeat(t, defaultRepeatLimits())
	feedRepeat(t, s, 0, 27*44100, func(n int64) float32 {
		if n >= 15*44100 {
			return repeatSample(n - 15*44100)
		}
		return repeatSample(n)
	})
	f, c, l, e := s.Finish()
	if e != nil {
		t.Fatal(e)
	}
	if len(f) != 1 || !c.Complete || c.ScannedUS != 27000000 {
		t.Fatalf("findings=%+v coverage=%+v limitations=%v", f, c, l)
	}
	if *f[0].StartUS != 0 || *f[0].EndUS != 12000000 || *f[0].SecondStartUS != 15000000 || *f[0].SecondEndUS != 27000000 || f[0].Verification != "pcm16-exact" {
		t.Fatalf("%+v", f[0])
	}
}
func TestRepeatSeedCollision(t *testing.T) {
	s := newTestRepeat(t, defaultRepeatLimits())
	calls := 0
	s.digest = func(b []byte, c int) [32]byte {
		calls++
		if calls == 1 || calls == 61 {
			return [32]byte{1}
		}
		return repeatDigest(b, c)
	}
	original := s.pcm
	reads := &countPCM{repeatPCM: original}
	s.pcm = reads
	feedRepeat(t, s, 0, 30*44100, repeatSample)
	f, _, _, e := s.Finish()
	if e != nil || len(f) != 0 {
		t.Fatalf("%v %v", f, e)
	}
	if reads.reads == 0 {
		t.Fatal("collision was not verified")
	}
}
func TestRepeatShortQuietAndDC(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sample func(int64) float32
	}{
		{"short", func(n int64) float32 { return repeatSample(n % (7 * 44100)) }},
		{"zero", func(int64) float32 { return 0 }},
		{"quiet", func(n int64) float32 { return repeatSample(n%(10*44100)) * 0.001 }},
		{"dc", func(int64) float32 { return 0.2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestRepeat(t, defaultRepeatLimits())
			feedRepeat(t, s, 0, 20*44100, tc.sample)
			f, _, _, e := s.Finish()
			if e != nil || len(f) != 0 {
				t.Fatalf("%v %v", f, e)
			}
		})
	}
}
func TestRepeatSyntheticGap(t *testing.T) {
	s := newTestRepeat(t, defaultRepeatLimits())
	feedRepeat(t, s, 0, 6*44100, repeatSample)
	gapEnd := int64(7*44100 + 123)
	if e := s.Break(gapEnd); e != nil {
		t.Fatal(e)
	}
	feedRepeat(t, s, gapEnd, 6*44100, func(n int64) float32 { return repeatSample(n + 6*44100) })
	feedRepeat(t, s, gapEnd+6*44100, 12*44100, repeatSample)
	f, _, _, e := s.Finish()
	if e != nil || len(f) != 0 {
		t.Fatalf("gap-spanning repetition: %v %v", f, e)
	}
	var offGrid int
	if e = s.db.QueryRow("SELECT count(*) FROM seeds WHERE start % 11025 != 0").Scan(&offGrid); e != nil || offGrid != 0 {
		t.Fatal("absolute grid drift", offGrid, e)
	}
}
func TestRepeatDeclaredLimits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shift int64
		gain  float32
	}{{"half-hop", 5513, 1}, {"gain", 0, 0.8}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestRepeat(t, defaultRepeatLimits())
			feedRepeat(t, s, 0, 28*44100, func(n int64) float32 {
				if n >= 15*44100+tc.shift {
					return repeatSample(n-15*44100-tc.shift) * tc.gain
				}
				return repeatSample(n)
			})
			_, _, l, e := s.Finish()
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(strings.Join(l, " "), "250 ms") || !strings.Contains(strings.Join(l, " "), "gain") || !strings.Contains(strings.Join(l, " "), "quantiz") || DetectorVersion != "repair-analysis-v1" {
				t.Fatalf("limitations missing: %v", l)
			}
		})
	}
}
func TestRepeatGridRounding(t *testing.T) {
	q, e := frameAtUS(250000)
	if e != nil || q != 11025 {
		t.Fatalf("%d %v", q, e)
	}
	for _, us := range []int64{1, 12345, 1234567, 100000012345} {
		q, e := frameAtUS(us)
		if e != nil || q != (us*44100+500000)/1000000 {
			t.Fatal(q, e)
		}
	}
	s := newTestRepeat(t, defaultRepeatLimits())
	feedRepeat(t, s, 0, 100*44100, func(n int64) float32 {
		if n >= 81*44100 {
			return repeatSample(n - 81*44100)
		}
		return repeatSample(n)
	})
	f, _, _, e := s.Finish()
	if e != nil || len(f) != 1 || *f[0].SecondStartUS != 81000000 || *f[0].EndUS != 19000000 {
		t.Fatalf("%v %v", f, e)
	}
}
func repeatDisk(t *testing.T, s *repeatScanner) (pcm, index int64) {
	t.Helper()
	es, e := os.ReadDir(s.dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range es {
		st, e := f.Info()
		if e != nil {
			t.Fatal(e)
		}
		if f.Name() == "pcm.s16le" {
			pcm += st.Size()
		} else {
			index += st.Size()
		}
	}
	return
}
func TestRepeatBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		limits     repeatLimits
	}{
		{"pcm", "repeat_pcm_limit", repeatLimits{pcmBytes: 44100 * 2 * 10, indexBytes: 2 << 20, pairs: 1000, occurrences: 8}},
		{"index", "repeat_index_limit", repeatLimits{pcmBytes: 8 << 20, indexBytes: 128 << 10, pairs: 1000, occurrences: 8}},
		{"pairs", "repeat_pair_limit", repeatLimits{pcmBytes: 8 << 20, indexBytes: 2 << 20, pairs: 1, occurrences: 8}},
		{"occurrences", "repeat_occurrence_limit", repeatLimits{pcmBytes: 8 << 20, indexBytes: 2 << 20, pairs: 1000, occurrences: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestRepeat(t, tc.limits)
			feedRepeat(t, s, 0, 80*44100, func(n int64) float32 { return repeatSample(n % (10 * 44100)) })
			f, c, l, e := s.Finish()
			if e != nil {
				t.Fatal(e)
			}
			p, d := repeatDisk(t, s)
			if p > tc.limits.pcmBytes || d > tc.limits.indexBytes || c.Complete || !strings.Contains(strings.Join(l, " "), tc.code) || len(f) > MaxFindings {
				t.Fatalf("pcm=%d db=%d coverage=%+v limits=%v findings=%d", p, d, c, l, len(f))
			}
		})
	}
}
func TestRepeatBoundedGrowth(t *testing.T) {
	for _, seconds := range []int64{30, 300} {
		s := newTestRepeat(t, repeatLimits{pcmBytes: 2 << 20, indexBytes: 1 << 20, pairs: 1000, occurrences: 8})
		feedRepeat(t, s, 0, seconds*44100, repeatSample)
		f, c, _, e := s.Finish()
		if e != nil || c.Complete {
			t.Fatal(c, e)
		}
		p, d := repeatDisk(t, s)
		if p > 2<<20 || d > 1<<20 || s.bufferBytes() > 2<<20 {
			t.Fatalf("unbounded pcm=%d index=%d buffers=%d", p, d, s.bufferBytes())
		}
		var cache int64
		if e = s.db.QueryRow("PRAGMA cache_size").Scan(&cache); e != nil || cache != -8192 {
			t.Fatalf("cache %d %v", cache, e)
		}
		for _, v := range f {
			if len(v.Detail) > 512 {
				t.Fatal("detail")
			}
		}
	}
}
func TestRepeatCancelENOSPCCleanup(t *testing.T) {
	t.Run("cancel-index", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		d := t.TempDir()
		s, e := newRepeatScanner(ctx, d, 1, defaultRepeatLimits())
		if e != nil {
			t.Fatal(e)
		}
		// Cancel after an eligible digest is computed, while indexing is
		// active and immediately before the SQLite insertion.
		s.digest = func(b []byte, c int) [32]byte { d := repeatDigest(b, c); cancel(); return d }
		b := make([]float32, 4093)
		for n := int64(0); n < 3*44100; n += int64(len(b)) {
			for i := range b {
				b[i] = repeatSample(n + int64(i))
			}
			e = s.Push(b, n)
			if e != nil {
				break
			}
		}
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
		es, _ := os.ReadDir(d)
		if len(es) != 0 {
			t.Fatal(es)
		}
	})
	t.Run("cancel-verify", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s, e := newRepeatScanner(ctx, t.TempDir(), 1, defaultRepeatLimits())
		if e != nil {
			t.Fatal(e)
		}
		feedRepeat(t, s, 0, 30*44100, func(n int64) float32 { return repeatSample(n % (15 * 44100)) })
		s.pcm = &faultPCM{repeatPCM: s.pcm, beforeRead: cancel}
		_, _, _, e = s.Finish()
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
		dir := s.dir
		if e = s.Close(); e != nil {
			t.Fatal(e)
		}
		if _, e = os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal(e)
		}
	})
	t.Run("disk-full-and-close", func(t *testing.T) {
		d := t.TempDir()
		source := filepath.Join(d, "source")
		os.WriteFile(source, []byte("immutable"), 0600)
		before, _ := os.ReadFile(source)
		s, e := newRepeatScanner(context.Background(), d, 1, defaultRepeatLimits())
		if e != nil {
			t.Fatal(e)
		}
		s.pcm = &faultPCM{repeatPCM: s.pcm, writeErr: syscall.ENOSPC, closeErr: errors.New("close-failure")}
		if e = s.Push([]float32{1}, 0); !errors.Is(e, syscall.ENOSPC) {
			t.Fatal(e)
		}
		if e = s.Close(); e == nil || !strings.Contains(e.Error(), "close-failure") {
			t.Fatal(e)
		}
		after, _ := os.ReadFile(source)
		if sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("source changed")
		}
		es, _ := os.ReadDir(d)
		if len(es) != 1 || es[0].Name() != "source" {
			t.Fatal(es)
		}
	})
}
func TestRepeatStreamingStress(t *testing.T) {
	if os.Getenv("ANALYSIS_STRESS") != "1" {
		t.Skip("requires >4 GiB scratch; opt in with ANALYSIS_STRESS=1")
	}
	s := newTestRepeat(t, defaultRepeatLimits())
	feedRepeat(t, s, 0, MaxPCMBytes/2+44100, repeatSample)
	_, c, l, e := s.Finish()
	if e != nil || c.Complete || !strings.Contains(strings.Join(l, " "), "repeat_pcm_limit") {
		t.Fatal(c, l, e)
	}
	p, d := repeatDisk(t, s)
	if p > MaxPCMBytes || d > MaxIndexBytes {
		t.Fatal(p, d)
	}
}

type faultPCM struct {
	repeatPCM
	writeErr, closeErr error
	beforeRead         func()
}

func (p *faultPCM) Write(b []byte) (int, error) {
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	return p.repeatPCM.Write(b)
}
func (p *faultPCM) ReadAt(b []byte, n int64) (int, error) {
	if p.beforeRead != nil {
		p.beforeRead()
	}
	return p.repeatPCM.ReadAt(b, n)
}
func (p *faultPCM) Close() error { return errors.Join(p.repeatPCM.Close(), p.closeErr) }

func TestRepeatCoalescingBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames int64
		sample func(int64) float32
	}{
		{"long", 143 * 44100, func(n int64) float32 {
			if n >= 73*44100 {
				return repeatSample(n - 73*44100)
			}
			return repeatSample(n)
		}},
		{"periodic", 80 * 44100, func(n int64) float32 { return repeatSample(n % (10 * 44100)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestRepeat(t, defaultRepeatLimits())
			feedRepeat(t, s, 0, tc.frames, tc.sample)
			f, _, _, e := s.Finish()
			if e != nil || len(f) == 0 {
				t.Fatal(f, e)
			}
			for _, v := range f {
				if *v.EndUS-*v.StartUS > 60000000 || *v.EndUS > *v.SecondStartUS {
					t.Fatalf("overlapping/overlong pair: %d..%d / %d..%d", *v.StartUS, *v.EndUS, *v.SecondStartUS, *v.SecondEndUS)
				}
			}
			if *f[0].StartUS != 0 {
				t.Fatal("earliest match lost", *f[0].StartUS)
			}
		})
	}
}
func TestRepeatIndexJournalBudget(t *testing.T) {
	limits := repeatLimits{pcmBytes: 40 << 20, indexBytes: 256 << 10, pairs: 1000, occurrences: 8}
	s := newTestRepeat(t, limits)
	feedRepeat(t, s, 0, 100*44100, repeatSample)
	// Force dirty pages while the real DELETE rollback journal is still open.
	tx, e := s.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec("UPDATE seeds SET spool=spool+1"); e != nil {
		t.Fatal(e)
	}
	p, d := repeatDisk(t, s)
	st, e := os.Stat(filepath.Join(s.dir, "index.sqlite-journal"))
	if e != nil || st.Size() == 0 {
		t.Fatalf("journal not exercised: %v", e)
	}
	t.Logf("open rollback transaction: PCM=%d index+journal=%d journal=%d budget=%d", p, d, st.Size(), limits.indexBytes)
	if d > limits.indexBytes || p > limits.pcmBytes {
		t.Fatal(p, d)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	rows, e := s.db.Query("EXPLAIN QUERY PLAN SELECT digest,start,spool FROM seeds ORDER BY digest,start")
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(detail, "TEMP") {
			t.Fatal("unbudgeted temp sort", detail)
		}
	}
	if e = rows.Close(); e != nil {
		t.Fatal(e)
	}
	feedRepeat(t, s, 100*44100, 300*44100, func(n int64) float32 { return repeatSample(n + 100*44100) })
	_, c, l, e := s.Finish()
	if e != nil || c.Complete || !strings.Contains(strings.Join(l, " "), "repeat_index_limit") {
		t.Fatal(c, l, e)
	}
	p, d = repeatDisk(t, s)
	t.Logf("after index saturation: PCM=%d index=%d coverage=%+v limitations=%v", p, d, c, l)
	if p > limits.pcmBytes || d > limits.indexBytes {
		t.Fatal(p, d)
	}
}

type countPCM struct {
	repeatPCM
	reads int
}

func (p *countPCM) ReadAt(b []byte, n int64) (int, error) { p.reads++; return p.repeatPCM.ReadAt(b, n) }

func TestRepeatQuantizationAndChannels(t *testing.T) {
	s, e := newRepeatScanner(context.Background(), t.TempDir(), 2, defaultRepeatLimits())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	values := []float32{-2, 2, -1, 1, -0.5, 0.5, 0, 0}
	if e = s.Push(values, 0); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 16)
	if _, e = s.pcm.ReadAt(b, 0); e != nil {
		t.Fatal(e)
	}
	want := []int16{-32767, 32767, -32767, 32767, -16384, 16384, 0, 0}
	for i, v := range want {
		if got := int16(binary.LittleEndian.Uint16(b[2*i:])); got != v {
			t.Fatalf("sample %d got %d want %d", i, got, v)
		}
	}
	if repeatDigest(b, 1) == repeatDigest(b, 2) {
		t.Fatal("channel count omitted from digest")
	}
	t.Logf("stereo repeat byte buffers=%d", s.bufferBytes())
	if s.bufferBytes() > 2<<20 {
		t.Fatal("stereo buffers", s.bufferBytes())
	}
}
func TestRepeatScratchUnusualPath(t *testing.T) {
	name := "question?mark#percent%"
	if runtime.GOOS == "windows" {
		name = "hash#percent% and space" // '?' cannot appear in a Windows file name
	}
	dir := filepath.Join(t.TempDir(), name)
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s, e := newRepeatScanner(context.Background(), dir, 1, defaultRepeatLimits())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(s.dir, "index.sqlite")); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
}

func TestRepeatDeterministicLimits(t *testing.T) {
	var previous []Finding
	var previousCoverage Coverage
	var previousLimits []string
	for i := 0; i < 2; i++ {
		limits := defaultRepeatLimits()
		limits.pairs = 1
		limits.occurrences = 2
		s := newTestRepeat(t, limits)
		feedRepeat(t, s, 0, 80*44100, func(n int64) float32 { return repeatSample(n % (10 * 44100)) })
		findings, coverage, warnings, e := s.Finish()
		if e != nil || coverage.Complete || len(findings) == 0 {
			t.Fatal(findings, coverage, e)
		}
		if i > 0 && (!reflect.DeepEqual(previous, findings) || previousCoverage != coverage || !reflect.DeepEqual(previousLimits, warnings)) {
			t.Fatal("nondeterministic bounded result")
		}
		previous, previousCoverage, previousLimits = findings, coverage, warnings
	}
}
