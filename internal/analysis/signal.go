package analysis

import "math"

type signalRun struct {
	kind       string
	start, end int64
}
type signalState struct {
	kind              string
	start, count, min int64
	pending           *signalRun
}
type signalScanner struct {
	channels    int
	position    int64
	clipCount   [2]int64
	clip, quiet signalState
	emit        func(signalRun)
}

func newSignalScanner(channels int, emit func(signalRun)) *signalScanner {
	return &signalScanner{channels: channels, clip: signalState{kind: "clipping", min: 1}, quiet: signalState{kind: "quiet", min: 220500}, emit: emit}
}
func (s *signalScanner) finish(state *signalState, end int64) {
	if state.count >= state.min {
		start := state.start
		if state.kind == "clipping" {
			start -= 2
		}
		run := signalRun{state.kind, start, end}
		if state.pending != nil && run.start-state.pending.end <= 4410 {
			state.pending.end = run.end
		} else {
			if state.pending != nil {
				s.emit(*state.pending)
			}
			state.pending = &run
		}
	}
	state.count = 0
}
func (s *signalScanner) consume(b []float32) {
	threshold := float32(math.Pow(10, -50.0/20))
	for i := 0; i < len(b); i += s.channels {
		quiet, clip := true, false
		for c := 0; c < s.channels; c++ {
			v := b[i+c]
			if v < 0 {
				v = -v
			}
			if v > threshold {
				quiet = false
			}
			if v >= float32(.999) {
				s.clipCount[c]++
			} else {
				s.clipCount[c] = 0
			}
			if s.clipCount[c] >= 3 {
				clip = true
			}
		}
		if clip {
			if s.clip.count == 0 {
				s.clip.start = s.position
			}
			s.clip.count++
		} else {
			s.finish(&s.clip, s.position)
		}
		if quiet {
			if s.quiet.count == 0 {
				s.quiet.start = s.position
			}
			s.quiet.count++
		} else {
			s.finish(&s.quiet, s.position)
		}
		s.position++
	}
}

// flush terminates a continuous source-backed signal (EOF, failure or synthetic
// silence). Ordinary source cuts do not call it, preserving cross-span runs.
func (s *signalScanner) flush() {
	s.finish(&s.clip, s.position)
	s.finish(&s.quiet, s.position)
	for _, state := range []*signalState{&s.clip, &s.quiet} {
		if state.pending != nil {
			s.emit(*state.pending)
			state.pending = nil
		}
	}
	s.clipCount = [2]int64{}
}
