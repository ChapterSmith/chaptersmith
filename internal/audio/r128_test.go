package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Float WAV is written independently of the production encoder, including the
// intentionally malformed nonfinite samples used to check decode rejection.
func r128Fixture(t *testing.T, channels int, samples []float32) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.wav")
	b := make([]byte, 44+4*len(samples))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 3)
	binary.LittleEndian.PutUint16(b[22:], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:], 44100)
	binary.LittleEndian.PutUint32(b[28:], uint32(44100*channels*4))
	binary.LittleEndian.PutUint16(b[32:], uint16(channels*4))
	binary.LittleEndian.PutUint16(b[34:], 32)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(samples)*4))
	for i, v := range samples {
		binary.LittleEndian.PutUint32(b[44+i*4:], math.Float32bits(v))
	}
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func r128Sine(channels, frames int, amplitude float64) []float32 {
	s := make([]float32, frames*channels)
	for i := 0; i < frames; i++ {
		v := float32(amplitude * math.Sin(2*math.Pi*1000*float64(i)/44100))
		for c := 0; c < channels; c++ {
			s[i*channels+c] = v
		}
	}
	return s
}
func r128Measure(t *testing.T, paths []string, dual bool) Loudness {
	t.Helper()
	m, e := MeasureR128(context.Background(), paths, dual)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestR128Reference(t *testing.T) {
	// ITU-R BS.1770 K-weighted 1 kHz unity channel response: stereo
	// sine with -20 dBFS peak has -3.0103 dB RMS and +3.0103 dB from
	// two channels; the K-weight/offset combination gives about -20 LUFS.
	// These analytic expectations do not come from MeasureR128.
	p := r128Fixture(t, 2, r128Sine(2, 3*44100, .1))
	m := r128Measure(t, []string{p}, false)
	if !m.HasIntegrated || m.Silent || math.Abs(m.IntegratedLUFS+20) > .2 || math.Abs(m.TruePeakDBTP+20) > .1 {
		t.Fatalf("reference: %+v", m)
	}
	// A quarter-rate sine at pi/4 phase samples at +/-0.5. Its continuous
	// amplitude is sqrt(0.5), giving -3.0103 dBTP vs -6.0206 dBFS sample
	// peak. Fade edges to avoid introducing discontinuity peaks.
	s := make([]float32, 3*44100)
	for i := range s {
		fade := math.Min(1, math.Min(float64(i)/4410, float64(len(s)-1-i)/4410))
		s[i] = float32(math.Sqrt(.5) * math.Sin(math.Pi*.5*float64(i)+math.Pi/4) * fade)
	}
	m = r128Measure(t, []string{r128Fixture(t, 1, s)}, false)
	if math.Abs(m.TruePeakDBTP+3.0103) > .15 || m.TruePeakDBTP <= -6.0206+2.8 {
		t.Fatalf("inter-sample peak: %+v", m)
	}
}
func TestR128DualMono(t *testing.T) {
	p := r128Fixture(t, 1, r128Sine(1, 3*44100, .1))
	a := r128Measure(t, []string{p}, false)
	b := r128Measure(t, []string{p}, true)
	if math.Abs(b.IntegratedLUFS-a.IntegratedLUFS-3.0103) > .02 || math.Abs(a.TruePeakDBTP-b.TruePeakDBTP) > 1e-6 {
		t.Fatalf("mono %+v dual %+v", a, b)
	}
	stereo := r128Fixture(t, 2, r128Sine(2, 3*44100, .1))
	a = r128Measure(t, []string{stereo}, false)
	b = r128Measure(t, []string{stereo}, true)
	if math.Abs(a.IntegratedLUFS-b.IntegratedLUFS) > 1e-6 {
		t.Fatal("dual mono altered stereo")
	}
}
func TestR128WholeBook(t *testing.T) {
	loud, quiet := r128Sine(2, 3*44100, .1), r128Sine(2, 3*44100, .01)
	a, b := r128Fixture(t, 2, loud), r128Fixture(t, 2, quiet)
	whole := r128Measure(t, []string{a, b}, false)
	joined := r128Measure(t, []string{r128Fixture(t, 2, append(loud, quiet...))}, false)
	x, y := r128Measure(t, []string{a}, false), r128Measure(t, []string{b}, false)
	if math.Abs(whole.IntegratedLUFS-joined.IntegratedLUFS) > .001 || math.Abs(whole.TruePeakDBTP-joined.TruePeakDBTP) > .001 {
		t.Fatalf("sequential %+v joined %+v", whole, joined)
	}
	if math.Abs(whole.IntegratedLUFS-(x.IntegratedLUFS+y.IntegratedLUFS)/2) < 5 {
		t.Fatal("independent LUFS averaging used")
	}
	// Two sub-block chapters together establish an integrated target.
	short := r128Fixture(t, 2, r128Sine(2, 11025, .1))
	if !r128Measure(t, []string{short, short}, false).HasIntegrated {
		t.Fatal("meter reset per chapter")
	}
}
func TestR128Undefined(t *testing.T) {
	for _, tc := range []struct {
		name   string
		s      []float32
		silent bool
	}{{"zeros", make([]float32, 44100), true}, {"short", r128Sine(1, 1000, .1), false}, {"below absolute gate", r128Sine(1, 44100, 1e-6), false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := r128Measure(t, []string{r128Fixture(t, 1, tc.s)}, true)
			if m.HasIntegrated || m.Silent != tc.silent {
				t.Fatalf("%+v", m)
			}
			if tc.silent && !math.IsInf(m.TruePeakDBTP, -1) {
				t.Fatalf("silent peak %+v", m)
			}
		})
	}
	for _, v := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		s := make([]float32, 44100)
		s[100] = v
		if _, e := MeasureR128(context.Background(), []string{r128Fixture(t, 1, s)}, false); e == nil {
			t.Fatal("nonfinite accepted")
		}
	}
}
func TestR128VeryShortPeak(t *testing.T) {
	for _, n := range []int{1, 2, 8, 16, 21, 22, 32, 33, 34, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := make([]float32, n)
			s[n-1] = .1
			a := r128Measure(t, []string{r128Fixture(t, 1, s)}, false)
			// A 0.1 impulse has -20 dBFS sample peak. Allow up to 6 dB of
			// interpolation overshoot, but never an unprocessed 0 dB option.
			if a.HasIntegrated || a.TruePeakDBTP < -20.001 || a.TruePeakDBTP > -14 {
				t.Fatalf("quiet short impulse: %+v", a)
			}
			s[n-1] = .2
			b := r128Measure(t, []string{r128Fixture(t, 1, s)}, false)
			if math.Abs(b.TruePeakDBTP-a.TruePeakDBTP-6.020599913) > .001 {
				t.Fatalf("peak did not scale linearly: %+v %+v", a, b)
			}
		})
	}
	for _, n := range []int{1, 2, 8, 32, 33, 34} {
		// Endpoint reflection of a constant finite signal remains constant.
		// Its independently known sample AND continuous peak is 0.1 (-20 dB).
		constant := make([]float32, n)
		for i := range constant {
			constant[i] = .1
		}
		m := r128Measure(t, []string{r128Fixture(t, 1, constant)}, false)
		if m.HasIntegrated || math.Abs(m.TruePeakDBTP+20) > .05 {
			t.Fatalf("%d-frame constant reference: %+v", n, m)
		}
		mono := make([]float32, n)
		mono[n-1] = .1
		stereo := make([]float32, 2*n)
		stereo[2*(n-1)] = .1
		stereo[2*(n-1)+1] = -.025
		a := r128Measure(t, []string{r128Fixture(t, 1, mono)}, false)
		b := r128Measure(t, []string{r128Fixture(t, 2, stereo)}, true)
		if b.HasIntegrated || math.Abs(a.TruePeakDBTP-b.TruePeakDBTP) > .001 {
			t.Fatalf("%d-frame channel shape altered peak: %+v %+v", n, a, b)
		}
	}

	a := r128Fixture(t, 1, []float32{.1})
	b := r128Fixture(t, 1, []float32{-.1})
	sequential := r128Measure(t, []string{a, b}, false)
	joined := r128Measure(t, []string{r128Fixture(t, 1, []float32{.1, -.1})}, false)
	if math.Abs(sequential.TruePeakDBTP-joined.TruePeakDBTP) > .001 || sequential.HasIntegrated {
		t.Fatalf("tiny chapters reset meter: %+v %+v", sequential, joined)
	}
}

func TestR128TailAndErrors(t *testing.T) {
	// Peak at actual EOF, in a frame shorter than the meter's 100 ms block.
	s := make([]float32, 44100+1)
	s[len(s)-1] = .9
	m := r128Measure(t, []string{r128Fixture(t, 1, s)}, true)
	if m.TruePeakDBTP < 20*math.Log10(.9)-.1 {
		t.Fatalf("last sample peak lost: %+v", m)
	}
	// Eight alternating quarter-rate samples at EOF have an interpolated
	// peak above their +/-0.5 sample peak, wholly within the resampler tail.
	tail := make([]float32, 44100+8)
	for i := 0; i < 8; i++ {
		tail[44100+i] = float32(math.Sqrt(.5) * math.Sin(math.Pi*.5*float64(i)+math.Pi/4))
	}
	m = r128Measure(t, []string{r128Fixture(t, 1, tail)}, false)
	if m.TruePeakDBTP <= 20*math.Log10(.5)+1 {
		t.Fatalf("terminal inter-sample peak lost: %+v", m)
	}
	for _, n := range []int{1, 2, 8, 100} {
		s := make([]float32, n)
		s[n-1] = .9
		m = r128Measure(t, []string{r128Fixture(t, 1, s)}, false)
		if m.HasIntegrated || m.TruePeakDBTP < 20*math.Log10(.9)-.1 {
			t.Fatalf("%d-frame short peak lost: %+v", n, m)
		}
	}

	p := r128Fixture(t, 1, r128Sine(1, 1000, .95))
	m = r128Measure(t, []string{p}, true)
	if m.HasIntegrated || m.TruePeakDBTP < -1 {
		t.Fatalf("short loud peak: %+v", m)
	}
	if _, e := MeasureR128(context.Background(), nil, false); e == nil {
		t.Fatal("empty paths accepted")
	}
	if _, e := MeasureR128(context.Background(), []string{p, "/missing-r128-source"}, false); e == nil {
		t.Fatal("decode failure lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := MeasureR128(ctx, []string{p}, false); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	// Invalid format must propagate native meter initialization failure.
	meter, e := newR128Meter(0, false)
	if meter != nil {
		meter.close()
	}
	if e == nil {
		t.Fatal("invalid filter configuration accepted")
	}
	stereo := r128Fixture(t, 2, r128Sine(2, 1000, .1))
	if _, e := MeasureR128(context.Background(), []string{p, stereo}, true); e == nil {
		t.Fatal("mixed channel layouts accepted")
	}
}
