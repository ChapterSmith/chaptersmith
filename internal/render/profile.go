package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"context"
	"errors"
	"fmt"
)

type encodingPlan struct {
	Channels, BitRate          int
	Format                     string
	CopySource, FallbackReason string
}

type aacCopyInspector func(context.Context, string, int) (int64, error)

func resolveEncoding(project book.BookProject) (encodingPlan, error) {
	return resolveEncodingWith(context.Background(), project, audio.AACCopyFrames)
}

// Export supplies its job context and per-call inspection boundary. Compatibility
// reasons are recoverable; source and native I/O failures remain errors.
func resolveEncodingWith(ctx context.Context, project book.BookProject, inspectAAC aacCopyInspector) (encodingPlan, error) {
	if e := ctx.Err(); e != nil {
		return encodingPlan{}, e
	}
	if e := book.Validate(project); e != nil {
		return encodingPlan{}, e
	}
	channels, e := resolveChannels(project, project.Export.Channels)
	if e != nil {
		return encodingPlan{}, e
	}
	plan := encodingPlan{Channels: channels, Format: project.Export.Format}
	profile := project.Export.Profile
	if profile == "preserve" {
		plan.FallbackReason, e = preserveReason(ctx, project, channels, inspectAAC)
		if e != nil {
			return encodingPlan{}, e
		}
		if plan.FallbackReason == "" {
			plan.CopySource = project.Sources[0].Path
			return plan, nil
		}
		profile = "standard"
	}
	if plan.Format == "flac" || plan.Format == "wav" {
		return plan, nil
	}
	rates := map[string][2]int{"economy": {48000, 80000}, "standard": {64000, 96000}, "high": {96000, 160000}}
	pair, ok := rates[profile]
	if !ok {
		return encodingPlan{}, fmt.Errorf("invalid encoding profile %q", profile)
	}
	plan.BitRate = pair[channels-1]
	return plan, nil
}
func preserveReason(ctx context.Context, p book.BookProject, channels int, inspectAAC aacCopyInspector) (string, error) {
	if p.Export.OutputMode != "book" {
		return "preserve requires whole-book output", nil
	}
	if p.Export.Format != "m4a" && p.Export.Format != "m4b" {
		return "preserve requires an AAC M4A/M4B destination", nil
	}
	if p.Export.Normalize {
		return "normalization requires encoding", nil
	}
	if len(p.Sources) != 1 {
		return "preserve requires exactly one source", nil
	}
	src := p.Sources[0]
	if src.Channels != channels {
		return "channel conversion requires encoding", nil
	}
	var cursor int64
	for _, ch := range p.Chapters {
		for _, s := range ch.Segments {
			if s.SilenceUS != 0 || s.SourceID != src.ID || s.StartUS != cursor {
				return "preserve requires intact source audio in continuous original order", nil
			}
			cursor = s.EndUS
		}
	}
	if cursor != src.DurationUS || cursor == 0 {
		return "preserve requires the complete source interval", nil
	}
	if e := book.CheckSources(p); e != nil {
		return "", e
	}
	// Bounded inspection uses the same job context as decode and publication.
	frames, e := inspectAAC(ctx, src.Path, channels)
	if canceled := ctx.Err(); canceled != nil {
		return "", errors.Join(e, canceled)
	}
	if e != nil {
		if audio.IsAACCopyIncompatible(e) {
			return e.Error(), nil
		}
		return "", e
	}
	want, e := frameAtUS(src.DurationUS)
	if e != nil {
		return "", e
	}
	if frames != want {
		return "source duration does not match the AAC sample clock", nil
	}
	return "", nil
}
