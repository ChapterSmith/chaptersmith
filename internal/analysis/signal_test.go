package analysis

import (
	"math"
	"testing"
)

func TestSignalThresholds(t *testing.T) {
	for _, tt := range []struct {
		name     string
		frames   int
		value    float32
		channels int
		kind     string
		want     int
	}{{"clip3", 3, .999, 1, "clipping", 1}, {"clip2", 2, .999, 1, "clipping", 0}, {"clipBelow", 3, .9989, 1, "clipping", 0}, {"quiet5", 220500, float32(math.Pow(10, -50.0/20)), 2, "quiet", 1}, {"quietShort", 220456, 0, 1, "quiet", 0}} {
		t.Run(tt.name, func(t *testing.T) {
			var got []signalRun
			s := newSignalScanner(tt.channels, func(r signalRun) { got = append(got, r) })
			b := make([]float32, tt.frames*tt.channels)
			for i := range b {
				b[i] = tt.value
			}
			mid := len(b) / tt.channels / 2 * tt.channels
			s.consume(b[:mid])
			s.consume(b[mid:])
			s.flush()
			n := 0
			for _, r := range got {
				if r.kind == tt.kind {
					n++
				}
			}
			if n != tt.want {
				t.Fatalf("%+v", got)
			}
		})
	}
	t.Run("channelContinuity", func(t *testing.T) {
		var got []signalRun
		s := newSignalScanner(2, func(r signalRun) { got = append(got, r) })
		s.consume([]float32{1, 0, 0, 1, 1, 0})
		s.flush()
		if len(got) != 0 {
			t.Fatal(got)
		}
	})
	for _, gap := range []int{4410, 4411} {
		var got []signalRun
		s := newSignalScanner(1, func(r signalRun) { got = append(got, r) })
		s.consume([]float32{1, 1, 1})
		s.consume(make([]float32, gap))
		s.consume([]float32{1, 1, 1})
		s.flush()
		want := 1
		if gap == 4411 {
			want = 2
		}
		if len(got) != want {
			t.Fatalf("gap %d: %+v", gap, got)
		}
	}
}
