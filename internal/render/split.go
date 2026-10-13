package render

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"

	"chaptersmith/internal/book"
)

// SuggestSplits listens to the whole edit and proposes chapter starts in the
// middle of its pauses. It never edits p; callers review the proposals and
// apply them with book.SplitChapters. Longer pauses win when two would make a
// chapter shorter than MinChapterUS, and existing chapter starts are kept.
// progress, when set, receives the position reached on the output timeline.
func SuggestSplits(ctx context.Context, p book.BookProject, opts book.SplitOptions, progress func(doneUS, totalUS int64)) ([]book.SplitProposal, error) {
	if opts.MinSilenceUS <= 0 || opts.MinChapterUS < 0 || math.IsNaN(opts.ThresholdDB) || math.IsInf(opts.ThresholdDB, 0) || opts.ThresholdDB > 0 {
		return nil, fmt.Errorf("chapter detection requires a positive pause length, a nonnegative chapter length and a finite threshold <= 0 dBFS")
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
	total := chapters[len(chapters)-1].EndFrame
	totalUS := book.DurationUS(p)
	minimum := (opts.MinSilenceUS/1000000)*44100 + ((opts.MinSilenceUS%1000000)*44100+999999)/1000000
	threshold := math.Pow(10, opts.ThresholdDB/20)

	type run struct{ start, end int64 }
	var runs []run
	runStart, next := int64(-1), int64(0)
	finish := func() {
		if runStart >= 0 && next-runStart >= minimum {
			runs = append(runs, run{runStart, next})
		}
		runStart = -1
	}
	err = streamSourceWindow(ctx, p, spans, channels, 0, total, func(pos int64, samples []float32) error {
		// A jump in position is inserted silence, which is not a pause in the recording.
		if pos != next {
			finish()
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
				finish()
			} else if runStart < 0 {
				runStart = frame
			}
			next = frame + 1
		}
		if progress != nil {
			progress(next/44100*1000000, totalUS)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	finish()

	gap := max(int64(1), (opts.MinChapterUS/1000000)*44100+((opts.MinChapterUS%1000000)*44100+999999)/1000000)
	taken := []int64{total}
	for _, chapter := range chapters {
		taken = append(taken, chapter.StartFrame)
	}
	slices.SortStableFunc(runs, func(a, b run) int { return cmp.Compare(b.end-b.start, a.end-a.start) })
	var chosen []run
	for _, r := range runs {
		middle := r.start + (r.end-r.start)/2
		if !slices.ContainsFunc(taken, func(other int64) bool { return frameDistance(middle, other) < gap }) {
			taken = append(taken, middle)
			chosen = append(chosen, r)
		}
	}
	slices.SortFunc(chosen, func(a, b run) int { return cmp.Compare(a.start, b.start) })
	proposals := make([]book.SplitProposal, 0, len(chosen))
	for _, r := range chosen {
		at, err := timelineUS(spans, r.start+(r.end-r.start)/2)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, book.SplitProposal{AtUS: at, SilenceUS: (r.end - r.start) * 1000000 / 44100})
	}
	return proposals, ctx.Err()
}
