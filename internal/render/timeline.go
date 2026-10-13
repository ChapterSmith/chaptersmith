package render

import (
	"chaptersmith/internal/book"
	"fmt"
	"math"
)

// sourceSpan keeps the accumulated render grid separate from domain duration.
// Per-segment rounding means a book's total frames need not equal Q(DurationUS).
type sourceSpan struct {
	Segment              book.Segment
	StartFrame, EndFrame int64
	StartUS, EndUS       int64
	SourceFrame          int64
}

func sourceTimeline(p book.BookProject) ([]sourceSpan, error) {
	var spans []sourceSpan
	var frame, us int64
	for _, chapter := range p.Chapters {
		for _, segment := range chapter.Segments {
			a, b, err := segmentFrames(segment)
			if err != nil {
				return nil, err
			}
			if frame > math.MaxInt64-(b-a) {
				return nil, fmt.Errorf("frame timeline overflow")
			}
			duration := segment.EndUS - segment.StartUS
			if segment.SilenceUS > 0 {
				duration = segment.SilenceUS
			}
			if duration <= 0 || us > math.MaxInt64-duration {
				return nil, fmt.Errorf("domain timeline overflow")
			}
			spans = append(spans, sourceSpan{Segment: segment, StartFrame: frame, EndFrame: frame + (b - a), StartUS: us, EndUS: us + duration, SourceFrame: a})
			frame += b - a
			us += duration
		}
	}
	return spans, nil
}

// timelineFrame maps a clipped domain window edge to the rendered grid.
func timelineFrame(spans []sourceSpan, us int64) (int64, error) {
	for _, span := range spans {
		if us < span.StartUS || us > span.EndUS {
			continue
		}
		if us == span.EndUS {
			return span.EndFrame, nil
		}
		offset := us - span.StartUS
		if span.Segment.SilenceUS > 0 {
			n, err := frameAtUS(offset)
			return min(span.EndFrame, span.StartFrame+n), err
		}
		n, err := frameAtUS(span.Segment.StartUS + offset)
		return min(span.EndFrame, span.StartFrame+(n-span.SourceFrame)), err
	}
	return 0, fmt.Errorf("domain time outside timeline")
}

// frameTimeUS is the nearest integer microsecond to an integer 44100 Hz frame.
func frameTimeUS(frame int64) (int64, error) {
	if frame < 0 {
		return 0, fmt.Errorf("negative frame")
	}
	whole := frame / 44100
	fraction := ((frame%44100)*1000000 + 22050) / 44100
	if whole > (math.MaxInt64-fraction)/1000000 {
		return 0, fmt.Errorf("frame time overflow")
	}
	return whole*1000000 + fraction, nil
}

func timelineUS(spans []sourceSpan, frame int64) (int64, error) {
	for _, span := range spans {
		if frame < span.StartFrame || frame >= span.EndFrame {
			continue
		}
		if frame == span.StartFrame {
			return span.StartUS, nil
		}
		if span.Segment.SilenceUS > 0 {
			return 0, fmt.Errorf("candidate lies in synthetic silence")
		}
		sourceUS, err := frameTimeUS(span.SourceFrame + (frame - span.StartFrame))
		if err != nil {
			return 0, err
		}
		// Endpoint rounding can differ by half a sample; keep the cut in-domain.
		sourceUS = max(span.Segment.StartUS, min(span.Segment.EndUS, sourceUS))
		return span.StartUS + (sourceUS - span.Segment.StartUS), nil
	}
	return 0, fmt.Errorf("frame outside source timeline")
}
