package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
)

type ExportResult struct {
	Paths               []string
	NormalizationReport string
}

// exportDeps holds the I/O boundaries for one transaction, never shared mutable hooks.
type exportDeps struct {
	inspectAAC aacCopyInspector
	render     func(context.Context, book.BookProject, []string, string, string) error
	encode     func(context.Context, string, string, audio.EncodeOptions) error
	copy       func(context.Context, string, string, audio.EncodeOptions) error
	measure    func(context.Context, []string, bool) (audio.Loudness, error)
	verify     func(context.Context, string, int, int64) error
	sync       func(string) error
	publish    func(string, string) error
}

func defaultExportDeps() exportDeps {
	return exportDeps{
		inspectAAC: audio.AACCopyFrames,
		render:     RenderPCM, encode: audio.EncodePCM, copy: audio.CopyAAC,
		measure: audio.MeasureR128, verify: verifyExportAudio,
		sync: syncExportFile, publish: publishNoReplace,
	}
}

// Export snapshots the project before callbacks or I/O. Publication is an atomic
// no-replace rename; cancellation after that commit point still returns success.
func Export(ctx context.Context, project book.BookProject, outputPath string, logger *log.Logger, onProgress func(current, total int, message string)) (ExportResult, error) {
	return exportWith(ctx, project, outputPath, logger, onProgress, defaultExportDeps())
}

type exportOutput struct {
	relative, final, staged string
	options                 audio.EncodeOptions
	whole                   bool
}

func exportWith(ctx context.Context, project book.BookProject, outputPath string, logger *log.Logger, onProgress func(int, int, string), deps exportDeps) (result ExportResult, err error) {
	project = book.Clone(project)
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = book.Validate(project); err != nil {
		return result, err
	}
	if len(project.Chapters) == 0 {
		return result, errors.New("project has no chapters")
	}
	if err = book.CheckSources(project); err != nil {
		return result, err
	}
	if strings.TrimSpace(outputPath) == "" {
		return result, errors.New("export destination is empty")
	}
	destination, err := filepath.Abs(outputPath)
	if err != nil {
		return result, err
	}
	if err = preflightDestination(destination, project); err != nil {
		return result, err
	}
	plan, err := resolveEncodingWith(ctx, project, deps.inspectAAC)
	if err != nil {
		return result, err
	}
	spans, err := chapterSpans(project)
	if err != nil {
		return result, err
	}
	outputs, err := exportOutputs(project, plan, spans, destination)
	if err != nil {
		return result, err
	}
	total := 3*len(outputs) + 5
	current := 0
	status := func(message string) {
		if logger != nil {
			logger.Print(message)
		}
		if onProgress != nil {
			onProgress(current, total, message)
		}
	}
	step := func(message string) { current++; status(message) }
	var reports []string
	report := func(message string) { reports = append(reports, message); status(message) }
	status("Project snapshot ready")
	if plan.FallbackReason != "" {
		report("Preserve fallback: " + plan.FallbackReason)
	}
	if plan.CopySource != "" {
		status("Checking compatible AAC stream for preservation")
	}
	if plan.Format == "flac" || plan.Format == "wav" {
		report("AAC bitrate profile is not applicable to lossless " + strings.ToUpper(plan.Format) + " output")
	}
	for _, out := range outputs {
		for _, warning := range audio.MetadataWarnings(out.options) {
			report(filepath.Base(out.final) + ": " + warning)
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".book-export-")
	if err != nil {
		return result, err
	}
	owned, err := os.Lstat(stage)
	// Windows reads a path-based FileInfo's identity on first comparison; take
	// it now, while the path still names the directory created above.
	_ = err == nil && os.SameFile(owned, owned)
	if err != nil {
		return result, err
	}
	committed := false
	defer func() {
		cleanupErr := removeOwnedStage(stage, owned)
		// Nothing after a successful rename can turn a published export into failure.
		if !committed {
			err = errors.Join(err, cleanupErr)
		} else if cleanupErr != nil && logger != nil {
			logger.Printf("Removing export staging directory: %v", cleanupErr)
		}
	}()
	payload := filepath.Join(stage, "payload")
	if project.Export.OutputMode != "book" {
		if err = os.Mkdir(payload, 0700); err != nil {
			return result, err
		}
		if project.Export.OutputMode == "both" {
			if err = os.Mkdir(filepath.Join(payload, "chapters"), 0700); err != nil {
				return result, err
			}
		}
	}
	for i := range outputs {
		outputs[i].staged = payload
		if outputs[i].relative != "" {
			outputs[i].staged = filepath.Join(payload, outputs[i].relative)
		}
	}
	pcm := filepath.Join(stage, "timeline.wav")
	if err = deps.render(ctx, project, nil, pcm, project.Export.Channels); err != nil {
		return result, fmt.Errorf("rendering book PCM: %w", err)
	}
	step("Rendered whole-book PCM")
	gain := gainPlan{}
	var input audio.Loudness
	if project.Export.Normalize {
		input, err = deps.measure(ctx, []string{pcm}, plan.Channels == 1)
		if err != nil {
			return result, fmt.Errorf("measuring book PCM: %w", err)
		}
		gain, err = normalizationGain(input)
		if err != nil {
			return result, err
		}
	}
	step("Encoding prepared")
	var final audio.Loudness
	maxPeak := math.Inf(-1)
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			for _, out := range outputs {
				if err = os.Remove(out.staged); err != nil {
					return result, err
				}
			}
		}
		for _, out := range outputs {
			if err = ctx.Err(); err != nil {
				return result, err
			}
			opts := out.options
			opts.GainDB = gain.GainDB
			if plan.CopySource != "" {
				err = deps.copy(ctx, plan.CopySource, out.staged, opts)
				if audio.IsAACCopyIncompatible(err) {
					if e := ctx.Err(); e != nil {
						return result, e
					}
					// CopyAAC closes and removes its own partial on failure.
					// Refuse unexpected leftovers instead of deleting an unowned file.
					if _, e := os.Lstat(out.staged); e == nil {
						return result, errors.Join(err, errors.New("failed AAC copy left a staging output"))
					} else if !os.IsNotExist(e) {
						return result, errors.Join(err, e)
					}
					report("Preserve fallback: " + err.Error())
					standard := project
					standard.Export.Profile = "standard"
					plan, err = resolveEncodingWith(ctx, standard, deps.inspectAAC)
					if err != nil {
						return result, err
					}
					opts.BitRate = plan.BitRate
					err = deps.encode(ctx, pcm, out.staged, opts)
				}
			} else {
				err = deps.encode(ctx, pcm, out.staged, opts)
			}
			if err != nil {
				return result, fmt.Errorf("encoding %s: %w", filepath.Base(out.final), err)
			}
			if err = deps.verify(ctx, out.staged, plan.Channels, opts.EndFrame-opts.StartFrame); err != nil {
				return result, fmt.Errorf("verifying %s: %w", filepath.Base(out.final), err)
			}
			step(fmt.Sprintf("Encoded %s (attempt %d)", filepath.Base(out.final), attempt+1))
		}
		if !project.Export.Normalize {
			break
		}
		maxPeak = math.Inf(-1)
		var chapterPaths []string
		hasBook := false
		for _, out := range outputs {
			measured, e := deps.measure(ctx, []string{out.staged}, plan.Channels == 1)
			if e != nil {
				return result, fmt.Errorf("measuring encoded %s: %w", filepath.Base(out.final), e)
			}
			if _, e = normalizationGain(measured); e != nil {
				return result, e
			}
			maxPeak = math.Max(maxPeak, measured.TruePeakDBTP)
			if out.whole {
				final = measured
				hasBook = true
			} else {
				chapterPaths = append(chapterPaths, out.staged)
			}
		}
		if !hasBook {
			final, err = deps.measure(ctx, chapterPaths, plan.Channels == 1)
			if err != nil {
				return result, err
			}
			if _, err = normalizationGain(final); err != nil {
				return result, err
			}
			maxPeak = math.Max(maxPeak, final.TruePeakDBTP)
		}
		if maxPeak <= -1.99 {
			break
		}
		if attempt == 2 {
			return result, fmt.Errorf("normalized output exceeds -2 dBTP ceiling after three attempts: %.2f dBTP", maxPeak)
		}
		reduction := maxPeak + 2 + .1
		gain.GainDB -= reduction
		gain.PeakLimited = true
		status(fmt.Sprintf("Encoded peak %.2f dBTP exceeds ceiling; reducing every output by %.2f dB", maxPeak, reduction))
	}
	if plan.CopySource != "" {
		report("Preserving compatible AAC stream")
	}
	if project.Export.Normalize {
		report(normalizationReport(input, gain))
		achieved := fmt.Sprintf("%.2f LUFS", final.IntegratedLUFS)
		if !final.HasIntegrated {
			achieved = "undefined LUFS (no gated blocks)"
			if final.Silent {
				achieved = "undefined LUFS (silent audio)"
			}
		}
		report(fmt.Sprintf("Final decoded whole book: %s; maximum output true peak %.2f dBTP; final fixed gain %+.2f dB", achieved, maxPeak, gain.GainDB))
	}
	for _, out := range outputs {
		if err = deps.sync(out.staged); err != nil {
			return result, fmt.Errorf("syncing output: %w", err)
		}
	}
	if err = os.Remove(pcm); err != nil {
		return result, err
	}
	step("Verified and synced outputs")
	// Repeat the conservative case-alias check, while the platform primitive
	// resolves exact-path races atomically. Never delete or replace a destination.
	if err = preflightDestination(destination, project); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = deps.publish(payload, destination); err != nil {
		return result, err
	}
	committed = true
	for _, out := range outputs {
		result.Paths = append(result.Paths, out.final)
	}
	result.NormalizationReport = strings.Join(reports, "\n")
	current = total
	status("Export complete")
	return result, nil
}

func exportOutputs(p book.BookProject, plan encodingPlan, spans []chapterSpan, destination string) ([]exportOutput, error) {
	opts := audio.EncodeOptions{Format: plan.Format, Channels: plan.Channels, BitRate: plan.BitRate, EndFrame: spans[len(spans)-1].EndFrame, Metadata: p.Metadata, CoverPath: p.CoverPath}
	var out []exportOutput
	if p.Export.OutputMode != "chapters" {
		for i, span := range spans {
			opts.Chapters = append(opts.Chapters, audio.ChapterMarker{Title: p.Chapters[i].Title, StartFrame: span.StartFrame, EndFrame: span.EndFrame})
		}
		path := destination
		relative := ""
		if p.Export.OutputMode == "both" {
			relative = "book." + plan.Format
			path = filepath.Join(destination, relative)
		}
		out = append(out, exportOutput{relative: relative, final: path, options: opts, whole: true})
	}
	if p.Export.OutputMode != "book" {
		for i, ch := range p.Chapters {
			name, e := chapterFilename(i+1, len(p.Chapters), ch.Title, plan.Format)
			if e != nil {
				return nil, e
			}
			relative := name
			if p.Export.OutputMode == "both" {
				relative = filepath.Join("chapters", name)
			}
			chapterOpts := opts
			chapterOpts.Chapters = nil
			chapterOpts.StartFrame = spans[i].StartFrame
			chapterOpts.EndFrame = spans[i].EndFrame
			chapterOpts.Metadata.Title = ch.Title
			chapterOpts.Metadata.Album = p.Metadata.Title
			chapterOpts.Track = fmt.Sprintf("%d/%d", i+1, len(p.Chapters))
			if chapterOpts.Format == "m4b" {
				chapterOpts.Format = "m4a"
			}
			path := filepath.Join(destination, relative)
			for _, previous := range out {
				if strings.EqualFold(previous.final, path) {
					return nil, fmt.Errorf("chapter filename collision: %s", name)
				}
			}
			out = append(out, exportOutput{relative: relative, final: path, options: chapterOpts})
		}
	}
	return out, nil
}

func preflightDestination(destination string, p book.BookProject) error {
	if _, e := os.Lstat(destination); e == nil {
		return fmt.Errorf("export destination already exists: %s", destination)
	} else if !os.IsNotExist(e) {
		return e
	}
	parent := filepath.Dir(destination)
	entries, e := os.ReadDir(parent)
	if e != nil {
		return fmt.Errorf("export parent must exist: %w", e)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), filepath.Base(destination)) {
			return fmt.Errorf("case-equivalent export destination already exists: %s", entry.Name())
		}
	}
	canonical, e := filepath.EvalSymlinks(parent)
	if e != nil {
		return e
	}
	candidate := filepath.Join(canonical, filepath.Base(destination))
	for _, source := range p.Sources {
		path, e := filepath.EvalSymlinks(source.Path)
		if e != nil {
			return e
		}
		if strings.EqualFold(candidate, path) {
			return errors.New("export destination aliases a source")
		}
	}
	return nil
}
func removeOwnedStage(path string, owned os.FileInfo) error {
	current, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !os.SameFile(current, owned) {
		return errors.New("export staging directory was replaced; refusing cleanup")
	}
	return os.RemoveAll(path)
}
func syncExportFile(path string) (err error) {
	f, e := os.OpenFile(path, os.O_RDWR, 0)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return f.Sync()
}
func verifyExportAudio(ctx context.Context, path string, channels int, wantFrames int64) error {
	var frames int64
	err := audio.DecodePCM(ctx, path, channels, 0, -1, func(b []float32) error {
		frames += int64(len(b) / channels)
		for _, v := range b {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return errors.New("nonfinite exported sample")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if frames != wantFrames {
		return fmt.Errorf("decoded frame count %d, expected %d", frames, wantFrames)
	}
	return nil
}
