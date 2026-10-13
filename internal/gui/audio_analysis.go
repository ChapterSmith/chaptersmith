package gui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"github.com/google/uuid"
)

type AudioAnalysisResult struct {
	RequestID string          `json:"requestId"`
	ProjectID string          `json:"projectId"`
	Origin    string          `json:"origin"`
	Revision  int64           `json:"revision"`
	Report    analysis.Report `json:"report"`
}
type AudioFindingPreview struct {
	RequestID  string `json:"requestId"`
	ReportID   string `json:"reportId"`
	MediaID    string `json:"mediaId"`
	URL        string `json:"url"`
	OriginUS   int64  `json:"originUs"`
	MarkerUS   int64  `json:"markerUs"`
	DurationUS int64  `json:"durationUs"`
}
type retainedAnalysis struct {
	project book.BookProject
	input   analysis.Input
	result  AudioAnalysisResult
	media   map[string]bool
}

// Cleanup failures must survive expected audition cancellation so the repair
// gate can distinguish failed disposal from an unavailable audition.
type analysisPreviewCleanupError struct{ error }

func (e *analysisPreviewCleanupError) Unwrap() error { return e.error }
func previewCleanupFailure(errs ...error) error {
	if err := errors.Join(errs...); err != nil {
		return &analysisPreviewCleanupError{err}
	}
	return nil
}

type analysisSourceError struct{ error }

func (e *analysisSourceError) Unwrap() error { return e.error }

type analyzeFunc func(context.Context, analysis.Input, analysis.Options, func(analysis.Progress)) (analysis.Report, error)

func analysisInput(project book.BookProject) (analysis.Input, error) {
	if err := book.Validate(project); err != nil {
		return analysis.Input{}, err
	}
	if err := book.CheckSources(project); err != nil {
		return analysis.Input{}, err
	}
	in := analysis.Input{Channels: 1}
	// Match finding audition: auto across all sources, independent of export settings.
	for _, s := range project.Sources {
		if s.Channels != 1 {
			in.Channels = 2
		}
		in.Sources = append(in.Sources, analysis.Source{ID: s.ID, Path: s.Path, DurationUS: s.DurationUS, SizeBytes: s.SizeBytes, MTimeNS: s.MTimeNS})
	}
	for _, c := range project.Chapters {
		for _, s := range c.Segments {
			in.Spans = append(in.Spans, analysis.Span{SourceID: s.SourceID, StartUS: s.StartUS, EndUS: s.EndUS, SilenceUS: s.SilenceUS})
		}
	}
	return in, nil
}

// progressInterval bounds how often a running scan reports to the UI. The
// decoder reports several times per second of audio, which would otherwise
// flood the event stream and stall the window on long books.
const progressInterval = 250 * time.Millisecond

// throttleProgress forwards the first report of each phase, every report that
// completes a phase, and otherwise at most one report per interval. Analysis
// reports from a single goroutine, so the state needs no lock.
func throttleProgress(interval time.Duration, forward func(analysis.Progress)) func(analysis.Progress) {
	if forward == nil {
		return nil
	}
	var last time.Time
	phase, started := "", false
	return func(p analysis.Progress) {
		now := time.Now()
		if started && p.Phase == phase && p.Current < p.Total && now.Sub(last) < interval {
			return
		}
		phase, last, started = p.Phase, now, true
		forward(p)
	}
}

func analyzeSnapshot(ctx context.Context, p book.BookProject, requestID, reportID, origin string, revision int64, cache string, analyze analyzeFunc, progress func(analysis.Progress)) (*retainedAnalysis, error) {
	snapshot := book.Clone(p)
	in, err := analysisInput(snapshot)
	if err != nil {
		return nil, err
	}
	report, err := analyze(ctx, in, analysis.Options{CacheDir: cache}, throttleProgress(progressInterval, progress))
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	report.ID = reportID
	return &retainedAnalysis{project: snapshot, input: in, result: AudioAnalysisResult{RequestID: requestID, ProjectID: p.ID, Revision: revision, Origin: origin, Report: report}, media: make(map[string]bool)}, nil
}

func repairAnalysisProject(ctx context.Context, inputPath string) (book.BookProject, error) {
	return repairProjectWithProbe(ctx, inputPath, audio.ProbeSource)
}
func repairProjectWithProbe(ctx context.Context, inputPath string, probe func(context.Context, string) (audio.SourceInfo, error)) (book.BookProject, error) {
	path, err := filepath.Abs(inputPath)
	if err != nil {
		return book.BookProject{}, err
	}
	info, err := probe(ctx, path)
	if err != nil {
		return book.BookProject{}, err
	}
	if err = ctx.Err(); err != nil {
		return book.BookProject{}, err
	}
	sourceID := uuid.NewString()
	p := book.BookProject{ID: uuid.NewString(), SchemaVersion: 1, Sources: []book.Source{{ID: sourceID, Path: path, DurationUS: info.DurationUS, SizeBytes: info.SizeBytes, MTimeNS: info.MTimeNS, SampleRate: info.SampleRate, Channels: info.Channels}}, Chapters: []book.Chapter{{ID: uuid.NewString(), Title: filepath.Base(path), Segments: []book.Segment{{SourceID: sourceID, EndUS: info.DurationUS}}}}, Export: book.ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"}}
	return p, book.Validate(p)
}

func (s *BookService) AnalyzeAudio(requestID string, expectedRevision int64) (result AudioAnalysisResult, err error) {
	ctx, p, err := s.beginOperation(requestID, "analyze", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishAnalysisOperation(requestID, err) }()
	s.mu.Lock()
	err = s.invalidateAudioLocked()
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	retained, err := analyzeSnapshot(ctx, p, requestID, uuid.NewString(), "book", expectedRevision, s.analysisOwner.Dir(), s.deps.Analyze, func(p analysis.Progress) { s.updateOperation(requestID, int(p.Current), int(p.Total), p.Phase) })
	if err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = s.checkRevisionLocked(expectedRevision); err != nil {
		return result, err
	}
	s.audioReport = retained
	s.operation.committed = true
	return cloneAnalysisResult(retained.result), nil
}
func (s *BookService) finishAnalysisOperation(requestID string, err error) {
	status, message := "succeeded", "Audio analysis ready"
	if err != nil {
		status, message = "failed", err.Error()
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
		}
	}
	s.finishOperation(requestID, status, message)
}
func (s *BookService) GetAudioAnalysis(reportID string) (AudioAnalysisResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.audioReport == nil || s.audioReport.result.Report.ID != reportID {
		return AudioAnalysisResult{}, fmt.Errorf("unknown audio report")
	}
	return cloneAnalysisResult(s.audioReport.result), nil
}
func (s *BookService) ReleaseAudioAnalysis(reportID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.audioReport == nil || s.audioReport.result.Report.ID != reportID {
		return fmt.Errorf("unknown audio report")
	}
	if s.operation != nil && s.operations[s.operation.requestID].Kind == "preview" {
		s.operation.cancel()
	}
	return s.invalidateAudioLocked()
}
func (s *BookService) invalidateAudioLocked() error {
	if s.audioReport == nil {
		return nil
	}
	err := releaseAnalysisMedia(s.Media, s.audioReport)
	s.audioReport = nil
	return err
}
func releaseAnalysisMedia(media *MediaRegistry, r *retainedAnalysis) error {
	var err error
	for id := range r.media {
		err = errors.Join(err, media.Release(id))
		delete(r.media, id)
	}
	return err
}
func (s *BookService) PreviewAudioFinding(requestID string, expectedRevision int64, reportID, findingID string, occurrence int) (result AudioFindingPreview, err error) {
	ctx, _, err := s.beginOperation(requestID, "preview", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishAnalysisOperation(requestID, err) }()
	s.mu.Lock()
	r := s.audioReport
	if r == nil || r.result.Report.ID != reportID || r.result.Revision != expectedRevision {
		s.mu.Unlock()
		return result, fmt.Errorf("unknown or stale audio report")
	}
	s.mu.Unlock()
	result, err = renderAnalysisPreview(ctx, r, requestID, findingID, occurrence, s.cacheDir, s.Media, s.deps.Preview)
	if err != nil {
		var changed *analysisSourceError
		if errors.As(err, &changed) {
			s.mu.Lock()
			if s.audioReport == r {
				err = errors.Join(err, s.invalidateAudioLocked())
			}
			s.mu.Unlock()
		}
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err == nil && (s.audioReport != r || s.closed) {
		err = context.Canceled
	}
	if err != nil {
		return AudioFindingPreview{}, errors.Join(err, s.Media.Release(result.MediaID))
	}
	r.media[result.MediaID] = true
	s.operation.committed = true
	return result, nil
}

// Crop the immutable edited output timeline, retaining source offsets and manual silence.
func analysisPreviewProject(p book.BookProject, r analysis.Report, findingID string, occurrence int) (book.BookProject, int64, int64, error) {
	if occurrence < 0 || occurrence > 1 {
		return book.BookProject{}, 0, 0, fmt.Errorf("invalid finding occurrence")
	}
	var start *int64
	for _, f := range r.Findings {
		if f.ID == findingID {
			start = f.StartUS
			if occurrence == 1 {
				start = f.SecondStartUS
			}
			break
		}
	}
	limit := min(book.DurationUS(p), r.ScannedUS)
	if start == nil || *start < 0 || *start >= limit {
		return book.BookProject{}, 0, 0, fmt.Errorf("audition unavailable for unknown, unlocated or unscanned finding")
	}
	origin := max(int64(0), *start-5000000)
	end := min(limit, origin+30000000)
	cropped := book.Clone(p)
	cropped.CoverPath = ""
	cropped.Export.Normalize = false
	chapter := book.Chapter{ID: uuid.NewString(), Title: "Audio finding"}
	var position int64
	for _, c := range p.Chapters {
		for _, seg := range c.Segments {
			duration := seg.SilenceUS
			if seg.SourceID != "" {
				duration = seg.EndUS - seg.StartUS
			}
			left, right := max(origin, position), min(end, position+duration)
			if right > left {
				if seg.SourceID == "" {
					chapter.Segments = append(chapter.Segments, book.Segment{SilenceUS: right - left})
				} else {
					chapter.Segments = append(chapter.Segments, book.Segment{SourceID: seg.SourceID, StartUS: seg.StartUS + left - position, EndUS: seg.StartUS + right - position})
				}
			}
			position += duration
		}
	}
	cropped.Chapters = []book.Chapter{chapter}
	return cropped, origin, *start - origin, book.Validate(cropped)
}
func renderAnalysisPreview(ctx context.Context, r *retainedAnalysis, requestID, findingID string, occurrence int, cache string, media *MediaRegistry, preview func(context.Context, book.BookProject, string, string) (string, error)) (AudioFindingPreview, error) {
	if err := analysis.CheckSources(ctx, r.input, r.result.Report.Sources); err != nil {
		if ctx.Err() != nil {
			return AudioFindingPreview{}, ctx.Err()
		}
		return AudioFindingPreview{}, &analysisSourceError{err}
	}
	p, origin, marker, err := analysisPreviewProject(r.project, r.result.Report, findingID, occurrence)
	if err != nil {
		return AudioFindingPreview{}, err
	}
	if ref, ok, err := streamPreview(media, p, p.Chapters[0].ID); err != nil {
		return AudioFindingPreview{}, fmt.Errorf("audition unavailable: %w", err)
	} else if ok {
		if err = ctx.Err(); err != nil {
			return AudioFindingPreview{}, errors.Join(err, previewCleanupFailure(media.Release(ref.ID)))
		}
		return AudioFindingPreview{RequestID: requestID, ReportID: r.result.Report.ID, MediaID: ref.ID, URL: ref.URL, OriginUS: origin, MarkerUS: marker, DurationUS: book.DurationUS(p)}, nil
	}
	root := filepath.Join(cache, "preview")
	path, err := preview(ctx, p, p.Chapters[0].ID, root)
	if err != nil {
		return AudioFindingPreview{}, fmt.Errorf("audition unavailable: %w", err)
	}
	owned, err := ownRenderedPreview(root, path)
	if err != nil {
		return AudioFindingPreview{}, fmt.Errorf("claiming rendered preview: %w", err)
	}
	ref, err := media.Register(path, "audio/mp4")
	if err != nil {
		return AudioFindingPreview{}, errors.Join(fmt.Errorf("registering preview: %w", err), previewCleanupFailure(owned.remove(), owned.root.Close()))
	}
	if err = owned.root.Close(); err != nil {
		return AudioFindingPreview{}, previewCleanupFailure(err, media.Release(ref.ID))
	}
	if err = ctx.Err(); err != nil {
		return AudioFindingPreview{}, errors.Join(err, previewCleanupFailure(media.Release(ref.ID)))
	}
	return AudioFindingPreview{RequestID: requestID, ReportID: r.result.Report.ID, MediaID: ref.ID, URL: ref.URL, OriginUS: origin, MarkerUS: marker, DurationUS: book.DurationUS(p)}, nil
}

// Copy slices/pointers for outbound DTOs while preserving private FileInfo identity in the retained report.
func cloneAnalysisResult(r AudioAnalysisResult) AudioAnalysisResult {
	r.Report.Sources = slices.Clone(r.Report.Sources)
	r.Report.Coverage = slices.Clone(r.Report.Coverage)
	r.Report.Limitations = slices.Clone(r.Report.Limitations)
	r.Report.Counts = maps.Clone(r.Report.Counts)
	r.Report.Findings = slices.Clone(r.Report.Findings)
	for i := range r.Report.Findings {
		f := &r.Report.Findings[i]
		f.Provenance = slices.Clone(f.Provenance)
		f.StartUS = cloneTime(f.StartUS)
		f.EndUS = cloneTime(f.EndUS)
		f.SecondStartUS = cloneTime(f.SecondStartUS)
		f.SecondEndUS = cloneTime(f.SecondEndUS)
		f.SourceStartUS = cloneTime(f.SourceStartUS)
		f.SourceEndUS = cloneTime(f.SourceEndUS)
		f.OriginalTimestampUS = cloneTime(f.OriginalTimestampUS)
	}
	return r
}
func cloneTime(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
