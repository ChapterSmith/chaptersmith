package book

import (
	"context"
	"fmt"
	"io"
	"log"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/meta"
	"github.com/google/uuid"
)

// Import builds a complete editable project without changing any input files.
// Cancellation is checked between libav operations; blocked local calls may finish first.
func Import(ctx context.Context, opts ImportOptions) (BookProject, error) {
	if err := ctx.Err(); err != nil {
		return BookProject{}, err
	}
	inputs, err := DiscoverInputs(opts, "")
	if err != nil {
		return BookProject{}, err
	}
	if err := ctx.Err(); err != nil {
		return BookProject{}, err
	}
	cache, err := importCacheDirectory(opts.CacheDir, inputs)
	if err != nil {
		return BookProject{}, err
	}
	project := BookProject{ID: uuid.NewString(), SchemaVersion: 1, Export: ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"}}
	probes := map[string]audio.SourceInfo{}
	ids := map[string]string{}
	for _, path := range inputs.Files {
		if err := ctx.Err(); err != nil {
			return BookProject{}, err
		}
		if _, ok := probes[path]; ok {
			continue
		}
		info, err := audio.ProbeSource(ctx, path)
		if err != nil {
			return BookProject{}, err
		}
		probes[path] = info
		ids[path] = uuid.NewString()
		project.Sources = append(project.Sources, Source{ID: ids[path], Path: path, DurationUS: info.DurationUS, SizeBytes: info.SizeBytes, MTimeNS: info.MTimeNS, SampleRate: info.SampleRate, Channels: info.Channels})
	}
	mode, err := importChapterMode(opts)
	if err != nil {
		return BookProject{}, err
	}
	chapters, err := chapter.ResolveSourceChapters(ctx, inputs.Files, inputs.Entries, probes, inputs.CuePath, mode)
	if err != nil {
		return BookProject{}, err
	}
	for _, c := range chapters {
		project.Chapters = append(project.Chapters, Chapter{ID: uuid.NewString(), Title: c.Title, Segments: []Segment{{SourceID: ids[inputs.Files[c.SourceIndex]], StartUS: c.StartUS, EndUS: c.EndUS}}})
	}
	if err := ctx.Err(); err != nil {
		return BookProject{}, err
	}
	logger := log.New(io.Discard, "", 0)
	project.Metadata = meta.CollectMetadataFromDir(inputs.WorkDir, inputs.Files[0], logger)
	if project.Metadata.Extra == nil {
		project.Metadata.Extra = map[string]string{}
	}
	if err := ctx.Err(); err != nil {
		return BookProject{}, err
	}
	if err := CheckSources(project); err != nil {
		return BookProject{}, fmt.Errorf("sources changed during import: %w", err)
	}
	project.CoverPath, err = meta.CacheCoverImage(inputs.WorkDir, inputs.Files[0], cache, logger)
	if err != nil {
		return BookProject{}, err
	}
	if err := ctx.Err(); err != nil {
		return BookProject{}, err
	}
	if err := Validate(project); err != nil {
		return BookProject{}, err
	}
	if err := CheckSources(project); err != nil {
		return BookProject{}, fmt.Errorf("sources changed during import: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return BookProject{}, err
	}
	return project, nil
}
