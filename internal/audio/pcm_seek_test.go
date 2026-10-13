package audio

import (
	"context"
	"math"
	"testing"
)

const seekFixtures = "../../testdata/formats/"

func decodeRange(t *testing.T, d *PCMDecoder, start, end int64) []float32 {
	t.Helper()
	var out []float32
	if err := d.Range(start, end, func(b []float32) error { out = append(out, b...); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// A seek must hand back exactly the samples that decoding from the origin
// produces, or decline; it may never return audio from a nearby position.
// Lossless sources match bit for bit. AAC lands on the same samples but its
// decoder carries state from earlier frames, so values differ far below
// audibility (about -90 dBFS measured).
func TestSeekNearMatchesDecodingFromOrigin(t *testing.T) {
	const start, end = 2 * 44100, 2*44100 + 22050
	for name, tolerance := range map[string]float64{"m4a/01.m4a": 1e-4, "flac/01.flac": 0, "wav/01.wav": 0} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			origin, err := OpenPCM(ctx, seekFixtures+name, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer origin.Close()
			want := decodeRange(t, origin, start, end)

			sought, err := OpenPCM(ctx, seekFixtures+name, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer sought.Close()
			ok, err := sought.SeekNear(start)
			if err != nil || !ok {
				t.Fatalf("SeekNear: ok=%v err=%v", ok, err)
			}
			if p := sought.Position(); p <= 0 || p > start {
				t.Fatalf("position %d after seek, want within (0, %d]", p, start)
			}
			got := decodeRange(t, sought, start, end)
			if len(got) != len(want) {
				t.Fatalf("got %d samples, want %d", len(got), len(want))
			}
			var worst float64
			for i := range want {
				worst = math.Max(worst, math.Abs(float64(got[i]-want[i])))
			}
			t.Logf("largest difference %g", worst)
			if worst > tolerance {
				t.Fatalf("largest difference %g exceeds %g", worst, tolerance)
			}
		})
	}
}

func TestSeekNearDeclinesInexactContainersAndTheStart(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"mp3/01.mp3", "ogg/01.ogg"} {
		d, err := OpenPCM(ctx, seekFixtures+name, 1)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := d.SeekNear(2 * 44100); ok || err != nil {
			t.Errorf("%s: ok=%v err=%v, want a clean refusal", name, ok, err)
		}
		// A refusal leaves the decoder usable from the origin.
		if got := decodeRange(t, d, 0, 1000); len(got) != 1000 {
			t.Errorf("%s: decoded %d samples after refusal", name, len(got))
		}
		d.Close()
	}
	d, err := OpenPCM(ctx, seekFixtures+"m4a/01.m4a", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if ok, err := d.SeekNear(100); ok || err != nil {
		t.Fatalf("near the start: ok=%v err=%v, want a clean refusal", ok, err)
	}
}
