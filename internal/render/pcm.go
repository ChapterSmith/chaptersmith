// Package render streams source-backed book timelines through the common PCM grid.
package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"context"
	"errors"
	"fmt"
	"math"
)

func frameAtUS(us int64) (int64, error) {
	if us < 0 {
		return 0, fmt.Errorf("negative time")
	}
	return (us/1000000)*44100 + ((us%1000000)*44100+500000)/1000000, nil
}

type chapterSpan struct {
	ID                   string
	StartFrame, EndFrame int64
}

func segmentFrames(s book.Segment) (int64, int64, error) {
	if s.SilenceUS > 0 {
		n, e := frameAtUS(s.SilenceUS)
		if e != nil {
			return 0, 0, e
		}
		if n == 0 {
			return 0, 0, fmt.Errorf("silence rounds to zero frames")
		}
		return 0, n, nil
	}
	a, e := frameAtUS(s.StartUS)
	if e != nil {
		return 0, 0, e
	}
	b, e := frameAtUS(s.EndUS)
	if e != nil {
		return 0, 0, e
	}
	if b <= a {
		return 0, 0, fmt.Errorf("audio interval rounds to zero frames")
	}
	return a, b, nil
}
func chapterSpans(p book.BookProject) ([]chapterSpan, error) {
	var out []chapterSpan
	var cursor int64
	for _, c := range p.Chapters {
		span := chapterSpan{ID: c.ID, StartFrame: cursor}
		for _, s := range c.Segments {
			a, b, e := segmentFrames(s)
			if e != nil {
				return nil, e
			}
			if cursor > math.MaxInt64-(b-a) {
				return nil, fmt.Errorf("frame timeline overflow")
			}
			cursor += b - a
		}
		span.EndFrame = cursor
		out = append(out, span)
	}
	return out, nil
}
func resolveChannels(p book.BookProject, s string) (int, error) {
	switch s {
	case "mono":
		return 1, nil
	case "stereo":
		return 2, nil
	case "auto":
		for _, src := range p.Sources {
			if src.Channels != 1 {
				return 2, nil
			}
		}
		return 1, nil
	}
	return 0, fmt.Errorf("invalid channels %q", s)
}

// RenderPCM writes selected chapters in project order. No IDs selects all chapters.
func RenderPCM(ctx context.Context, p book.BookProject, ids []string, path, channels string) error {
	p = book.Clone(p)
	if e := book.Validate(p); e != nil {
		return e
	}
	if len(p.Chapters) == 0 {
		return fmt.Errorf("project has no chapters")
	}
	if e := book.CheckSources(p); e != nil {
		return e
	}
	if _, e := chapterSpans(p); e != nil {
		return e
	}
	ch, e := resolveChannels(p, channels)
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for _, c := range p.Chapters {
		known[c.ID] = true
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if !known[id] || selected[id] {
			return fmt.Errorf("unknown or duplicate chapter ID %q", id)
		}
		selected[id] = true
	}
	paths := map[string]string{}
	for _, s := range p.Sources {
		paths[s.ID] = s.Path
	}
	return audio.WriteRF64(ctx, path, ch, func(emit func([]float32) error) (err error) {
		var decoder *audio.PCMDecoder
		var source string
		var cursor int64
		defer func() {
			if decoder != nil {
				err = errors.Join(err, decoder.Close())
			}
		}()
		zeros := make([]float32, 16384*ch)
		for _, c := range p.Chapters {
			if len(ids) > 0 && !selected[c.ID] {
				continue
			}
			for _, s := range c.Segments {
				if e := ctx.Err(); e != nil {
					return e
				}
				a, b, e := segmentFrames(s)
				if e != nil {
					return e
				}
				if s.SilenceUS > 0 {
					for b > 0 {
						n := min(b, 16384)
						if e := emit(zeros[:int(n)*ch]); e != nil {
							return e
						}
						b -= n
					}
					continue
				}
				if decoder == nil || source != s.SourceID || a < cursor {
					if decoder != nil {
						if e := decoder.Close(); e != nil {
							return e
						}
					}
					decoder, e = audio.OpenPCM(ctx, paths[s.SourceID], ch)
					if e != nil {
						return e
					}
					source = s.SourceID
				}
				if e = decoder.Range(a, b, emit); e != nil {
					return fmt.Errorf("chapter %q: %w", c.ID, e)
				}
				cursor = b
			}
		}
		return nil
	})
}
