package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// PCMProblem is diagnostic evidence on the source's audible 44100 Hz grid.
// A nil SourceFrame denotes source-only evidence, never a guessed output time.
type PCMProblem struct {
	Kind                string `json:"kind"`
	Detail              string `json:"detail"`
	SourceFrame         *int64 `json:"sourceFrame"`
	OriginalTimestampUS *int64 `json:"originalTimestampUs"`
}
type PCMInspection struct {
	EmittedFrames int64 `json:"emittedFrames"`
	Complete      bool  `json:"complete"`
}
type pcmMediaError struct{ error }

func (e *pcmMediaError) Unwrap() error { return e.error }

// A bounds error may be joined with a seek/close error. Only a tree composed
// entirely of known media errors may be downgraded to diagnostic evidence.
func onlyMediaError(err error) bool {
	if _, ok := err.(*pcmMediaError); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !onlyMediaError(e) {
				return false
			}
		}
		return true
	}
	return false
}

type timestampTracker struct {
	expected int64
	have     bool
}

func (t *timestampTracker) observe(start, end, pts, duration int64, valid bool, selectedStart, selectedEnd int64, emit func(PCMProblem)) {
	selected := end > selectedStart && (selectedEnd < 0 || start < selectedEnd)
	if !valid {
		t.have = false
		if selected && emit != nil {
			emit(PCMProblem{Kind: "missing_timestamp", Detail: "Decoded audio frame has no timestamp; location unavailable."})
		}
		return
	}
	// A discontinuity is located at the decoded frame start, not anywhere
	// that its samples overlap. Never relocate evidence onto a trim boundary.
	selectedEvent := start >= selectedStart && (selectedEnd < 0 || start < selectedEnd)
	if selectedEvent && t.have && (pts > t.expected && uint64(pts)-uint64(t.expected) > 100000 || pts < t.expected && uint64(t.expected)-uint64(pts) > 100000) && emit != nil {
		location := start
		original := pts
		emit(PCMProblem{Kind: "timestamp_discontinuity", Detail: "Decoded audio timestamp differs from the expected end by more than 100000 us; audible damage is not implied.", SourceFrame: &location, OriginalTimestampUS: &original})
	}
	t.have = duration >= 0 && pts <= math.MaxInt64-duration
	if t.have {
		t.expected = pts + duration
	}
}

// InspectPCM shares the strict decoder and resampler. Only media rejection is
// recoverable diagnostic evidence; file, callback and cancellation errors remain
// errors. It never pads an early EOF with fabricated samples.
func InspectPCM(ctx context.Context, path string, channels int, startFrame, endFrame int64, consume func([]float32) error, onProblem func(PCMProblem)) (result PCMInspection, err error) {
	if e := ctx.Err(); e != nil {
		return result, e
	}
	_, e := bookPCMFormat(channels)
	if e != nil {
		return result, e
	}
	if startFrame < 0 || endFrame < -1 || endFrame != -1 && endFrame <= startFrame {
		return result, errors.New("invalid PCM inspection range")
	}
	file, e := os.Open(path)
	if e != nil {
		return result, e
	}
	st, e := file.Stat()
	if e != nil {
		return result, errors.Join(e, file.Close())
	}
	if !st.Mode().IsRegular() {
		return result, errors.Join(errors.New("PCM input must be a regular file"), file.Close())
	}
	return inspectPCMInput(ctx, path, channels, startFrame, endFrame, consume, onProblem, file)
}

func inspectPCMInput(ctx context.Context, path string, channels int, startFrame, endFrame int64, consume func([]float32) error, onProblem func(PCMProblem), input interface {
	io.ReadSeeker
	io.Closer
}) (result PCMInspection, err error) {
	if e := ctx.Err(); e != nil {
		return result, errors.Join(e, input.Close())
	}
	f, e := bookPCMFormat(channels)
	if e != nil {
		return result, errors.Join(e, input.Close())
	}
	if startFrame < 0 || endFrame < -1 || endFrame != -1 && endFrame <= startFrame {
		return result, errors.Join(errors.New("invalid PCM inspection range"), input.Close())
	}
	d, e := openPCMInputMode(ctx, path, f, input, true)
	if e != nil {
		return result, e
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	tracker := timestampTracker{}
	d.observe = func(start, end, pts, duration int64, valid bool) {
		tracker.observe(start, end, pts, duration, valid, startFrame, endFrame, onProblem)
	}
	var sinkErr error
	e = d.Range(startFrame, endFrame, func(b []float32) error {
		if consume != nil {
			if sinkErr = consume(b); sinkErr != nil {
				return sinkErr
			}
		}
		result.EmittedFrames += int64(len(b) / channels)
		return nil
	})
	if sinkErr != nil {
		return result, sinkErr
	}
	if e == nil {
		result.Complete = true
		return result, nil
	}
	var media *pcmMediaError
	if !errors.As(e, &media) {
		return result, e
	}
	var location *int64
	if d.position >= startFrame {
		v := d.position
		location = &v
	}
	if onProblem != nil {
		onProblem(PCMProblem{Kind: "decode_error", Detail: fmt.Sprintf("Audio decoding stopped: %v", e), SourceFrame: location})
	}
	return result, nil
}
