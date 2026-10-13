package render

import (
	"chaptersmith/internal/audio"
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestGainPolicy(t *testing.T) {
	for _, tc := range []struct {
		i, p, g float64
		limited bool
	}{{-24, -10, 6, false}, {-24, -3, 1, true}, {-12, -1, -6, false}} {
		m := audio.Loudness{IntegratedLUFS: tc.i, TruePeakDBTP: tc.p, HasIntegrated: true}
		g, e := normalizationGain(m)
		if e != nil || g.GainDB != tc.g || !g.TargetDefined || g.PeakLimited != tc.limited {
			t.Fatalf("%+v: %+v %v", m, g, e)
		}
		report := normalizationReport(m, g)
		if !strings.Contains(report, "LUFS") || !strings.Contains(report, "dBTP") || !strings.Contains(report, "dB") {
			t.Fatal(report)
		}
	}
}
func TestR128UndefinedGain(t *testing.T) {
	for _, tc := range []struct {
		m       audio.Loudness
		gain    float64
		limited bool
	}{{audio.Loudness{IntegratedLUFS: -70, TruePeakDBTP: -20}, 0, false}, {audio.Loudness{IntegratedLUFS: -70, TruePeakDBTP: -1}, -1, true}, {audio.Loudness{IntegratedLUFS: -70, TruePeakDBTP: math.Inf(-1), Silent: true}, 0, false}} {
		g, e := normalizationGain(tc.m)
		if e != nil || g.GainDB != tc.gain || g.TargetDefined || g.PeakLimited != tc.limited {
			t.Fatalf("%+v: %+v %v", tc.m, g, e)
		}
		report := normalizationReport(tc.m, g)
		if !strings.Contains(report, "undefined") {
			t.Fatal(report)
		}
		if g.PeakLimited && !strings.Contains(report, "peak-limited") {
			t.Fatal(report)
		}
	}
	for _, m := range []audio.Loudness{{IntegratedLUFS: math.NaN()}, {IntegratedLUFS: math.Inf(1), HasIntegrated: true}, {TruePeakDBTP: math.NaN()}, {TruePeakDBTP: math.Inf(1)}, {TruePeakDBTP: math.Inf(-1)}, {Silent: true, TruePeakDBTP: -10}, {Silent: true, HasIntegrated: true, TruePeakDBTP: math.Inf(-1)}} {
		if _, e := normalizationGain(m); e == nil {
			t.Fatalf("invalid measurement accepted: %+v", m)
		}
	}
}
func TestR128VeryShortNoInventedAttenuation(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{1, 2, 8, 100} {
		p := filepath.Join(t.TempDir(), "short.wav")
		s := make([]float32, n)
		s[n-1] = .1
		if e := audio.WriteRF64(ctx, p, 1, func(emit func([]float32) error) error { return emit(s) }); e != nil {
			t.Fatal(e)
		}
		m, e := audio.MeasureR128(ctx, []string{p}, true)
		if e != nil {
			t.Fatal(e)
		}
		g, e := normalizationGain(m)
		if e != nil || g.GainDB != 0 || g.TargetDefined || g.PeakLimited {
			t.Fatalf("%d-frame quiet audio received invented gain: %+v %+v %v", n, m, g, e)
		}
	}
}

func TestFixedGainDynamics(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "source.wav"), filepath.Join(dir, "encoded.wav")
	s := make([]float32, 2*44100)
	for i := range s {
		a := .1
		if i >= 44100 {
			a = .025
		}
		s[i] = float32(a * math.Sin(2*math.Pi*1000*float64(i)/44100))
	}
	if e := audio.WriteRF64(ctx, src, 1, func(emit func([]float32) error) error { return emit(s) }); e != nil {
		t.Fatal(e)
	}
	measured, e := audio.MeasureR128(ctx, []string{src}, true)
	if e != nil {
		t.Fatal(e)
	}
	gain, e := normalizationGain(measured)
	if e != nil {
		t.Fatal(e)
	}
	if e := audio.EncodePCM(ctx, src, dst, audio.EncodeOptions{Format: "wav", Channels: 1, EndFrame: -1, GainDB: gain.GainDB}); e != nil {
		t.Fatal(e)
	}
	var sums [2]float64
	var n int
	if e := audio.DecodePCM(ctx, dst, 1, 0, -1, func(b []float32) error {
		for _, v := range b {
			if n >= len(s) {
				t.Fatal("excess samples")
			}
			sums[n/44100] += float64(v) * float64(v)
			n++
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if n != len(s) || math.Abs(math.Sqrt(sums[0]/sums[1])-4) > 1e-4 {
		t.Fatalf("fixed gain changed dynamics: %v n=%d", sums, n)
	}
}
