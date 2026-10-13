package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/meta"
)

// loadChapterMarkers adapts integer source timelines to the legacy muxer.
func loadChapterMarkers(ctx context.Context, inputs book.ImportInputs, chapterSource string) ([]chapter.Chapter, error) {
	return resolveChapterMarkers(ctx, inputs, chapterSource, nil)
}

func loadJoinedChapterMarkers(ctx context.Context, inputs book.ImportInputs, chapterSource string, joined audio.JoinResult) ([]chapter.Chapter, error) {
	if joined.SampleRate <= 0 || len(joined.Frames) != len(inputs.Files) {
		return nil, fmt.Errorf("invalid audible join timeline")
	}
	return resolveChapterMarkers(ctx, inputs, chapterSource, &joined)
}

func resolveChapterMarkers(ctx context.Context, inputs book.ImportInputs, chapterSource string, joined *audio.JoinResult) ([]chapter.Chapter, error) {
	probes := make(map[string]audio.SourceInfo)
	bases := make([]int64, len(inputs.Files))
	var total int64
	for i, path := range inputs.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, ok := probes[path]
		if !ok {
			var err error
			info, err = audio.ProbeSource(ctx, path)
			if err != nil {
				return nil, err
			}
			probes[path] = info
		}
		bases[i] = total
		duration := info.DurationUS
		if joined != nil {
			duration = joined.Frames[i]
		}
		if duration < 0 || duration > math.MaxInt64-total {
			return nil, fmt.Errorf("source %s: total timeline length overflows", path)
		}
		total += duration
	}
	local, err := chapter.ResolveSourceChapters(ctx, inputs.Files, inputs.Entries, probes, inputs.CuePath, chapterSource)
	if err != nil {
		return nil, err
	}
	markers := make([]chapter.Chapter, len(local))
	for i, c := range local {
		localStart := c.StartUS
		if joined != nil {
			localStart, err = scaleTimeline(localStart, int64(joined.SampleRate), 1000000)
			if err != nil {
				return nil, err
			}
			if localStart >= joined.Frames[c.SourceIndex] {
				return nil, fmt.Errorf("chapter %q starts outside audible source", c.Title)
			}
		}
		if localStart > math.MaxInt64-bases[c.SourceIndex] {
			return nil, fmt.Errorf("chapter start overflow")
		}
		start := bases[c.SourceIndex] + localStart
		if joined != nil {
			start, err = scaleTimeline(start, 1000000, int64(joined.SampleRate))
			if err != nil {
				return nil, err
			}
		}
		markers[i] = chapter.Chapter{Title: c.Title, StartTime: float64(start) / 1000000}
	}
	return markers, nil
}

// rejectInputOutputs protects selected sources and original sidecars.
func rejectInputOutputs(inputs book.ImportInputs, logger *log.Logger, outputs ...string) error {
	sources := append([]string(nil), inputs.Files...)
	for _, sidecar := range []string{inputs.CuePath, inputs.PlaylistPath} {
		if sidecar != "" {
			sources = append(sources, sidecar)
		}
	}
	if cover, ok := meta.FindCoverImage(inputs.WorkDir, inputs.Files[0], logger); ok {
		sources = append(sources, cover)
	}
	for _, output := range outputs {
		if err := rejectSourceOutput(output, sources); err != nil {
			return err
		}
	}
	return nil
}

// rejectSourceOutput rejects both names and existing filesystem aliases before writes.
func rejectSourceOutput(output string, sources []string) error {
	absolute, err := outputAbsoluteName(output)
	if err != nil {
		return fmt.Errorf("resolving output: %w", err)
	}
	outInfo, err := os.Stat(absolute)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat output %s: %w", output, err)
	}
	for _, source := range sources {
		sourceAbsolute, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		if absolute == sourceAbsolute {
			return fmt.Errorf("output %s aliases source %s", output, source)
		}
		if outInfo != nil {
			info, err := os.Stat(sourceAbsolute)
			if err != nil {
				return fmt.Errorf("stat source %s: %w", source, err)
			}
			if os.SameFile(outInfo, info) {
				return fmt.Errorf("output %s aliases source %s", output, source)
			}
		}
	}
	return nil
}

// Resolve parent symlinks (such as macOS /var) while keeping the final filename
// distinct, so fix naming does not turn a user-created alias into an overwrite.
func outputAbsoluteName(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if os.IsNotExist(err) {
		return absolute, nil
	}
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

// scaleTimeline rounds once on the integer clock, checking both products and sum.
func scaleTimeline(value, numerator, denominator int64) (int64, error) {
	if value < 0 || numerator <= 0 || denominator <= 0 {
		return 0, fmt.Errorf("invalid timeline conversion")
	}
	q, r := value/denominator, value%denominator
	if q > math.MaxInt64/numerator || r > (math.MaxInt64-denominator/2)/numerator {
		return 0, fmt.Errorf("timeline conversion overflow")
	}
	whole, fraction := q*numerator, (r*numerator+denominator/2)/denominator
	if whole > math.MaxInt64-fraction {
		return 0, fmt.Errorf("timeline conversion overflow")
	}
	return whole + fraction, nil
}
