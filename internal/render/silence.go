package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"context"
	"errors"
	"fmt"
	"math"
)

// SuggestBoundaries proposes transfers at existing chapter starts. Callers must
// review and apply them with book.ShiftBoundary; this function never edits p.
// Typical options are ±5 seconds, -40 dBFS, and at least .5 seconds of silence.
func SuggestBoundaries(ctx context.Context, p book.BookProject, opts book.SilenceOptions) ([]book.BoundaryProposal, error) {
	if opts.WindowUS <= 0 || opts.MinSilenceUS <= 0 || math.IsNaN(opts.ThresholdDB) || math.IsInf(opts.ThresholdDB, 0) || opts.ThresholdDB > 0 {
		return nil, fmt.Errorf("silence requires positive window/minimum and finite threshold <= 0 dBFS")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p = book.Clone(p)
	if err := book.Validate(p); err != nil {
		return nil, err
	}
	if len(p.Chapters) == 0 {
		return nil, fmt.Errorf("project has no chapters")
	}
	if err := book.CheckSources(p); err != nil {
		return nil, err
	}
	spans, err := sourceTimeline(p)
	if err != nil {
		return nil, err
	}
	chapters, err := chapterSpans(p)
	if err != nil {
		return nil, err
	}
	channels, err := resolveChannels(p, "auto")
	if err != nil {
		return nil, err
	}
	// Ceiling, rather than Q(), enforces a run lasting at least MinSilenceUS.
	minimum := (opts.MinSilenceUS/1000000)*44100 + ((opts.MinSilenceUS%1000000)*44100+999999)/1000000
	threshold := math.Pow(10, opts.ThresholdDB/20)
	totalUS := book.DurationUS(p)
	if opts.ChapterID != "" {
		known := false
		for _, chapter := range chapters {
			known = known || chapter.ID == opts.ChapterID
		}
		if !known {
			return nil, fmt.Errorf("unknown chapter ID %q", opts.ChapterID)
		}
	}
	working := p
	reader := newWindowReader(ctx, p, channels)
	defer reader.Close()
	proposals := make([]book.BoundaryProposal, 0)
	for i := 1; i < len(chapters); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.ChapterID != "" && chapters[i].ID != opts.ChapterID {
			continue
		}
		oldUS, err := book.ChapterStartUS(p, chapters[i].ID)
		if err != nil {
			return nil, err
		}
		lowUS := max(int64(0), oldUS-min(oldUS, opts.WindowUS))
		highUS := oldUS + min(totalUS-oldUS, opts.WindowUS)
		low, err := timelineFrame(spans, lowUS)
		if err != nil {
			return nil, err
		}
		high, err := timelineFrame(spans, highUS)
		if err != nil {
			return nil, err
		}
		if high-low < minimum {
			continue
		}
		scanner := silenceScanner{channels: channels, threshold: threshold, minimum: minimum, boundary: chapters[i].StartFrame, best: -1, runStart: -1}
		if err := reader.stream(spans, low, high, scanner.consume); err != nil {
			return nil, err
		}
		scanner.finishRun()
		if scanner.best < 0 {
			continue
		}
		newUS, err := timelineUS(spans, scanner.best)
		if err != nil {
			return nil, err
		}
		if newUS == oldUS || newUS < lowUS || newUS > highUS {
			continue
		}
		updated, err := book.ShiftBoundary(working, chapters[i].ID, newUS, "transfer")
		if err != nil {
			continue
		}
		working = updated
		proposals = append(proposals, book.BoundaryProposal{ChapterID: chapters[i].ID, OldStartUS: oldUS, NewStartUS: newUS})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return proposals, nil
}

type silenceScanner struct {
	channels             int
	threshold            float64
	minimum, boundary    int64
	runStart, next, best int64
	bestDistance         int64
	bestHalf             bool
}

func (s *silenceScanner) consume(pos int64, samples []float32) error {
	if pos != s.next {
		s.finishRun()
	}
	for offset := 0; offset < len(samples); offset += s.channels {
		quiet := true
		for _, value := range samples[offset : offset+s.channels] {
			if math.Abs(float64(value)) > s.threshold {
				quiet = false
				break
			}
		}
		frame := pos + int64(offset/s.channels)
		if quiet {
			if s.runStart < 0 {
				s.runStart = frame
			}
		} else {
			s.finishRun()
		}
		s.next = frame + 1
	}
	return nil
}

func (s *silenceScanner) finishRun() {
	if s.runStart >= 0 && s.next-s.runStart >= s.minimum {
		midpoint := s.runStart + (s.next-s.runStart)/2
		distance := frameDistance(midpoint, s.boundary)
		// Compare true midpoints before rounding odd runs to an integer frame.
		// Whole/half components avoid float loss and doubled-frame overflow.
		half := (s.next-s.runStart)%2 != 0
		if half && midpoint < s.boundary {
			distance--
		}
		closer := distance < s.bestDistance || distance == s.bestDistance && !half && s.bestHalf
		tied := distance == s.bestDistance && half == s.bestHalf
		if s.best < 0 || closer || tied && midpoint < s.best {
			s.best = midpoint
			s.bestDistance = distance
			s.bestHalf = half
		}
	}
	s.runStart = -1
}

func frameDistance(a, b int64) int64 {
	if a < b {
		return b - a
	}
	return a - b
}

// windowReader streams neighbourhoods of the edit while keeping its decoder
// between calls, so a sweep over ascending positions reads each source once
// even when the container cannot seek. Close it after use.
type windowReader struct {
	ctx      context.Context
	paths    map[string]string
	channels int
	decoder  *audio.PCMDecoder
	sourceID string
}

func newWindowReader(ctx context.Context, p book.BookProject, channels int) *windowReader {
	r := &windowReader{ctx: ctx, channels: channels, paths: make(map[string]string, len(p.Sources))}
	for _, source := range p.Sources {
		r.paths[source.ID] = source.Path
	}
	return r
}

func (r *windowReader) Close() error {
	if r.decoder == nil {
		return nil
	}
	err := r.decoder.Close()
	r.decoder = nil
	return err
}

func (r *windowReader) open(sourceID string) (err error) {
	if err = r.Close(); err != nil {
		return err
	}
	r.decoder, err = audio.OpenPCM(r.ctx, r.paths[sourceID], r.channels)
	r.sourceID = sourceID
	return err
}

// stream passes the source-backed samples between two output frames to
// consume. The decoder seeks to the neighbourhood where the container allows
// it and otherwise decodes forward; it reopens for backwards ranges. Gaps in
// frame positions expose synthetic silence so a scanner can split its runs.
func (r *windowReader) stream(spans []sourceSpan, low, high int64, consume func(int64, []float32) error) error {
	for _, span := range spans {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if span.Segment.SilenceUS > 0 || span.EndFrame <= low || span.StartFrame >= high {
			continue
		}
		start := max(low, span.StartFrame)
		end := min(high, span.EndFrame)
		a := span.SourceFrame + (start - span.StartFrame)
		b := a + (end - start)
		if r.decoder == nil || r.sourceID != span.Segment.SourceID || a < r.decoder.Position() {
			if err := r.open(span.Segment.SourceID); err != nil {
				return err
			}
		}
		if a-r.decoder.Position() > streamSeekGap {
			if _, err := r.decoder.SeekNear(a); err != nil {
				// A failed seek leaves the decoder unusable; start over from origin.
				if err := r.open(span.Segment.SourceID); err != nil {
					return err
				}
			}
		}
		position := start
		if err := r.decoder.Range(a, b, func(samples []float32) error {
			if err := consume(position, samples); err != nil {
				return err
			}
			position += int64(len(samples) / r.channels)
			return nil
		}); err != nil {
			return fmt.Errorf("silence source %q: %w", r.sourceID, err)
		}
	}
	return r.ctx.Err()
}

func streamSourceWindow(ctx context.Context, p book.BookProject, spans []sourceSpan, channels int, low, high int64, consume func(int64, []float32) error) (err error) {
	reader := newWindowReader(ctx, p, channels)
	defer func() { err = errors.Join(err, reader.Close()) }()
	return reader.stream(spans, low, high, consume)
}

// MeasureChapterPauses reports the silence the recording already has on each
// side of every chapter start after the first, looking up to windowUS either
// way. It never edits p; book.SetChapterPauses can then replace those pauses.
func MeasureChapterPauses(ctx context.Context, p book.BookProject, thresholdDB float64, windowUS int64) (pauses []book.ChapterPause, err error) {
	if windowUS <= 0 || math.IsNaN(thresholdDB) || math.IsInf(thresholdDB, 0) || thresholdDB > 0 {
		return nil, fmt.Errorf("measuring pauses requires a positive window and a finite threshold <= 0 dBFS")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p = book.Clone(p)
	if err := book.Validate(p); err != nil {
		return nil, err
	}
	if err := book.CheckSources(p); err != nil {
		return nil, err
	}
	spans, err := sourceTimeline(p)
	if err != nil {
		return nil, err
	}
	chapters, err := chapterSpans(p)
	if err != nil {
		return nil, err
	}
	channels, err := resolveChannels(p, "auto")
	if err != nil {
		return nil, err
	}
	threshold := math.Pow(10, thresholdDB/20)
	window := (windowUS/1000000)*44100 + (windowUS%1000000)*44100/1000000
	reader := newWindowReader(ctx, p, channels)
	defer func() { err = errors.Join(err, reader.Close()) }()
	pauses = make([]book.ChapterPause, 0, len(chapters))
	for i := 1; i < len(chapters); i++ {
		boundary := chapters[i].StartFrame
		low, high := max(chapters[i-1].StartFrame, boundary-window), min(chapters[i].EndFrame, boundary+window)
		// The quiet run that contains the boundary, grown outwards from it.
		runStart, next := int64(-1), low
		var before, after int64
		closeRun := func() {
			if runStart >= 0 && runStart <= boundary && next >= boundary {
				before, after = boundary-runStart, next-boundary
			}
			runStart = -1
		}
		if err := reader.stream(spans, low, high, func(pos int64, samples []float32) error {
			if pos != next {
				closeRun()
			}
			for offset := 0; offset < len(samples); offset += channels {
				quiet := true
				for _, value := range samples[offset : offset+channels] {
					if math.Abs(float64(value)) > threshold {
						quiet = false
						break
					}
				}
				frame := pos + int64(offset/channels)
				if !quiet {
					next = frame
					closeRun()
				} else if runStart < 0 {
					runStart = frame
				}
				next = frame + 1
			}
			return nil
		}); err != nil {
			return nil, err
		}
		closeRun()
		pauses = append(pauses, book.ChapterPause{ChapterID: chapters[i].ID, BeforeUS: before * 1000000 / 44100, AfterUS: after * 1000000 / 44100})
	}
	return pauses, ctx.Err()
}
