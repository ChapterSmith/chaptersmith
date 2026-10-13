package gui

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/book"
)

type SuggestionsResult struct {
	RequestID string                  `json:"requestId"`
	ProjectID string                  `json:"projectId"`
	Revision  int64                   `json:"revision"`
	Proposals []book.BoundaryProposal `json:"proposals"`
}

func (s *BookService) SuggestBoundaries(requestID string, expectedRevision int64, opts book.SilenceOptions) (result SuggestionsResult, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "suggest", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishChapterOperation(requestID, "Suggestions ready for review", err) }()
	if opts == (book.SilenceOptions{}) {
		opts = book.SilenceOptions{WindowUS: 5000000, ThresholdDB: -40, MinSilenceUS: 500000}
	}
	if err = book.CheckSources(snapshot); err != nil {
		return result, err
	}
	proposals, err := s.deps.Suggest(ctx, snapshot, opts)
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
	s.operation.committed = true
	return SuggestionsResult{RequestID: requestID, ProjectID: snapshot.ID, Revision: expectedRevision, Proposals: append([]book.BoundaryProposal{}, proposals...)}, nil
}

func (s *BookService) finishChapterOperation(requestID, message string, err error) {
	status := "succeeded"
	if err != nil {
		status, message = "failed", err.Error()
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
		}
	}
	s.finishOperation(requestID, status, message)
}

// ApplyBoundaries validates against the original timeline and commits the whole batch once.
func (s *BookService) ApplyBoundaries(expectedRevision int64, proposals []book.BoundaryProposal) (ProjectView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMutationLocked(expectedRevision); err != nil {
		return ProjectView{}, err
	}
	if len(proposals) == 0 {
		return s.viewLocked(), nil
	}
	ordered := append([]book.BoundaryProposal(nil), proposals...)
	seen := make(map[string]bool, len(proposals))
	for _, proposal := range ordered {
		if seen[proposal.ChapterID] {
			return ProjectView{}, fmt.Errorf("duplicate chapter proposal")
		}
		seen[proposal.ChapterID] = true
		if err := validateBoundaryProposal(s.project, proposal); err != nil {
			return ProjectView{}, err
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].OldStartUS < ordered[j].OldStartUS })
	candidate := book.Clone(s.project)
	for _, proposal := range ordered {
		var err error
		candidate, err = book.ShiftBoundary(candidate, proposal.ChapterID, proposal.NewStartUS, "transfer")
		if err != nil {
			return ProjectView{}, err
		}
	}
	return s.commitEditLocked(candidate)
}

func validateBoundaryProposal(project book.BookProject, proposal book.BoundaryProposal) error {
	start, err := book.ChapterStartUS(project, proposal.ChapterID)
	if err != nil {
		return err
	}
	if start == 0 {
		return fmt.Errorf("the first chapter boundary cannot move")
	}
	if start != proposal.OldStartUS {
		return fmt.Errorf("stale boundary proposal")
	}
	// Neighbor bounds are checked sequentially by ApplyBoundaries: a proposal
	// can depend on an earlier transfer in the same batch. Audition is unchanged audio.
	if proposal.NewStartUS <= 0 || proposal.NewStartUS >= book.DurationUS(project) {
		return fmt.Errorf("proposed boundary must be inside the book")
	}
	return nil
}

func (s *BookService) PreviewBoundary(requestID string, expectedRevision int64, proposal book.BoundaryProposal) (result PreviewResult, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "preview", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishChapterOperation(requestID, "Preview ready — press Play", err) }()
	window, origin, err := boundaryPreviewProject(snapshot, proposal)
	if err != nil {
		return result, err
	}
	return s.renderPreview(ctx, window, window.Chapters[0].ID, PreviewResult{RequestID: requestID, ProjectID: snapshot.ID, Revision: expectedRevision, DurationUS: book.DurationUS(window), OriginUS: origin, OldMarkerUS: proposal.OldStartUS - origin, NewMarkerUS: proposal.NewStartUS - origin})
}

// SplitSuggestions are proposed chapter starts for the revision they were found in.
type SplitSuggestions struct {
	RequestID string               `json:"requestId"`
	ProjectID string               `json:"projectId"`
	Revision  int64                `json:"revision"`
	Proposals []book.SplitProposal `json:"proposals"`
}

// SuggestSplits listens to the whole book for pauses that could start chapters.
// It reports progress and changes nothing; splits are applied as an edit.
func (s *BookService) SuggestSplits(requestID string, expectedRevision int64, opts book.SplitOptions) (result SplitSuggestions, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "suggest", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishChapterOperation(requestID, "Chapter breaks ready for review", err) }()
	if err = book.CheckSources(snapshot); err != nil {
		return result, err
	}
	report := throttleProgress(progressInterval, func(p analysis.Progress) {
		s.updateOperation(requestID, int(p.Current), int(p.Total), "Listening for pauses")
	})
	proposals, err := s.deps.Splits(ctx, snapshot, opts, func(doneUS, totalUS int64) {
		report(analysis.Progress{Phase: "pauses", Current: doneUS / 1000000, Total: totalUS / 1000000})
	})
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
	s.operation.committed = true
	return SplitSuggestions{RequestID: requestID, ProjectID: snapshot.ID, Revision: expectedRevision, Proposals: append([]book.SplitProposal{}, proposals...)}, nil
}

// PauseMeasurements are the pauses found at chapter starts for one revision.
type PauseMeasurements struct {
	RequestID string              `json:"requestId"`
	ProjectID string              `json:"projectId"`
	Revision  int64               `json:"revision"`
	Pauses    []book.ChapterPause `json:"pauses"`
}

// PauseOptions bound the search for existing silence at chapter starts.
type PauseOptions struct {
	ThresholdDB float64 `json:"thresholdDb"`
	WindowUS    int64   `json:"windowUs"`
}

// MeasureChapterPauses finds the silence already present at each chapter start
// so an edit can replace it. It changes nothing.
func (s *BookService) MeasureChapterPauses(requestID string, expectedRevision int64, opts PauseOptions) (result PauseMeasurements, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "suggest", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishChapterOperation(requestID, "Pauses measured", err) }()
	if err = book.CheckSources(snapshot); err != nil {
		return result, err
	}
	pauses, err := s.deps.Pauses(ctx, snapshot, opts.ThresholdDB, opts.WindowUS)
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
	s.operation.committed = true
	return PauseMeasurements{RequestID: requestID, ProjectID: snapshot.ID, Revision: expectedRevision, Pauses: append([]book.ChapterPause{}, pauses...)}, nil
}

// PreviewPosition plays up to five seconds either side of a place in the book.
func (s *BookService) PreviewPosition(requestID string, expectedRevision int64, atUS int64) (result PreviewResult, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "preview", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() { s.finishChapterOperation(requestID, "Preview ready — press Play", err) }()
	if atUS <= 0 || atUS >= book.DurationUS(snapshot) {
		return result, fmt.Errorf("preview position is outside the book")
	}
	window, origin, err := previewWindow(snapshot, atUS, atUS)
	if err != nil {
		return result, err
	}
	return s.renderPreview(ctx, window, window.Chapters[0].ID, PreviewResult{RequestID: requestID, ProjectID: snapshot.ID, Revision: expectedRevision, DurationUS: book.DurationUS(window), OriginUS: origin, OldMarkerUS: atUS - origin, NewMarkerUS: atUS - origin})
}

// boundaryPreviewProject clips copied output segments, preserving source offsets and silence.
func boundaryPreviewProject(project book.BookProject, proposal book.BoundaryProposal) (book.BookProject, int64, error) {
	if err := validateBoundaryProposal(project, proposal); err != nil {
		return book.BookProject{}, 0, err
	}
	return previewWindow(project, min(proposal.OldStartUS, proposal.NewStartUS), max(proposal.OldStartUS, proposal.NewStartUS))
}

// previewWindow is the edit from five seconds before low to five seconds
// after high, as a one-chapter project, and where that window begins.
func previewWindow(project book.BookProject, low, high int64) (book.BookProject, int64, error) {
	origin := low - min(low, int64(5000000))
	end := high + min(book.DurationUS(project)-high, int64(5000000))
	window := book.Clone(project)
	chapter := book.Chapter{ID: "boundary-preview", Title: "Boundary preview"}
	var cursor int64
	for _, sourceChapter := range project.Chapters {
		for _, segment := range sourceChapter.Segments {
			duration := segment.EndUS - segment.StartUS
			if segment.SourceID == "" {
				duration = segment.SilenceUS
			}
			from, to := max(origin, cursor), min(end, cursor+duration)
			if from < to {
				if segment.SourceID == "" {
					segment.SilenceUS = to - from
				} else {
					segment.StartUS += from - cursor
					segment.EndUS = segment.StartUS + to - from
				}
				chapter.Segments = append(chapter.Segments, segment)
			}
			cursor += duration
		}
	}
	window.Chapters = []book.Chapter{chapter}
	if err := book.Validate(window); err != nil {
		return book.BookProject{}, 0, err
	}
	return window, origin, nil
}
