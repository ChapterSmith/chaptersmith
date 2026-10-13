package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/analysiscache"
	"chaptersmith/internal/app"
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"chaptersmith/internal/render"
	"chaptersmith/internal/store"
	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type RepairAnalysisState struct {
	ReportID string               `json:"reportId"`
	Status   string               `json:"status"`
	Phase    string               `json:"phase"`
	Error    string               `json:"error"`
	TaskID   int64                `json:"taskId"`
	Current  int64                `json:"current"`
	Total    int64                `json:"total"`
	Result   *AudioAnalysisResult `json:"result"`
}
type repairDependencies struct {
	Analyze func(context.Context, analysis.Input, analysis.Options, func(analysis.Progress)) (analysis.Report, error)
	Probe   func(context.Context, string) (audio.SourceInfo, error)
	Preview func(context.Context, book.BookProject, string, string) (string, error)
	Emit    func(string, any)
	Run     func(context.Context, app.Config) error
}
type fixResponse struct {
	promptID, choice, reportID string
	analyze                    bool
}
type repairAnalysis struct {
	state    RepairAnalysisState
	report   *retainedAnalysis
	ctx      context.Context
	decision chan bool
	chosen   *bool
	failure  error
	preview  *repairPreview
}
type repairPreview struct {
	cancel     context.CancelFunc
	done       chan struct{}
	cleanupErr error // Published before done closes; excludes expected audition cancellation.
}

// A supplied media registry is borrowed. Only the coordinated factory owns it.
func NewMergerServiceWithAnalysis(db *store.DB, media *MediaRegistry, cacheDir string) *MergerService {
	s := &MergerService{store: db, media: media, cacheDir: cacheDir}
	s.deps = repairDependencies{Analyze: analysis.Analyze, Probe: audio.ProbeSource, Preview: render.Preview, Emit: func(name string, p any) {
		if a := application.Get(); a != nil {
			a.Event.Emit(name, p)
		}
	}, Run: func(ctx context.Context, cfg app.Config) error {
		return app.New(cfg, log.New(os.Stdout, "", log.LstdFlags)).Run(ctx)
	}}
	return s
}

func (s *MergerService) promptFix(ctx context.Context, cfg *mergeConfig, filename string) string {
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		s.mu.Unlock()
		return "stop"
	}
	cfg.promptID = uuid.NewString()
	cfg.promptAnswered = false
	id := cfg.promptID
	s.mu.Unlock()
	s.deps.Emit("task-prompt-fix", map[string]any{"filename": filename, "promptId": id})
	select {
	case <-ctx.Done():
		return "stop"
	case response := <-cfg.fixResponse:
		if ctx.Err() != nil {
			return "stop"
		}
		s.mu.Lock()
		cfg.response = response
		s.mu.Unlock()
		return response.choice
	}
}
func (s *MergerService) RespondFixPromptOptions(promptID, choice string, analyze bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.respondFixLocked(promptID, choice, analyze)
}
func (s *MergerService) respondFixLocked(promptID, choice string, analyze bool) (string, error) {
	if s.closed || s.cfg == nil || s.cfg.ctx.Err() != nil || promptID == "" || s.cfg.promptID != promptID || s.cfg.promptAnswered {
		return "", fmt.Errorf("unknown, stale or answered repair prompt")
	}
	if choice != "fix" && choice != "continue" && choice != "stop" {
		return "", fmt.Errorf("invalid repair choice")
	}
	response := fixResponse{promptID: promptID, choice: choice, analyze: analyze && choice == "fix"}
	if response.analyze {
		if s.media == nil {
			return "", fmt.Errorf("repair analysis media unavailable")
		}
		if s.analysisOwner == nil {
			owner, err := analysiscache.New(s.cacheDir)
			if err != nil {
				return "", err
			}
			s.analysisOwner = owner
			s.ownsAnalysisOwner = true
		}
		response.reportID = uuid.NewString()
		s.repair = &repairAnalysis{state: RepairAnalysisState{ReportID: response.reportID, TaskID: s.cfg.taskID, Status: "analyzing", Phase: "probe"}, ctx: s.cfg.ctx, decision: make(chan bool, 1)}
	}
	s.cfg.promptAnswered = true
	s.cfg.fixResponse <- response
	return response.reportID, nil
}
func (s *MergerService) beforeFix(ctx context.Context, cfg *mergeConfig, path string) (err error) {
	s.mu.Lock()
	response := cfg.response
	r := s.repair
	s.mu.Unlock()
	if !response.analyze {
		return ctx.Err()
	}
	if r == nil || r.state.ReportID != response.reportID {
		return fmt.Errorf("repair analysis no longer current")
	}
	defer func() {
		if err != nil {
			cleanup := s.stopRepairPreview(r)
			s.mu.Lock()
			if r.report != nil {
				if releaseErr := releaseAnalysisMedia(s.media, r.report); releaseErr != nil {
					cleanup = errors.Join(cleanup, releaseErr)
				}
			}
			if cleanup != nil && !errors.Is(err, cleanup) {
				err = errors.Join(err, cleanup)
			}
			r.state.Error = err.Error()
			r.state.Status = "failed"
			if errors.Is(err, context.Canceled) {
				r.state.Status = "cancelled"
			}
			s.mu.Unlock()
			s.emitRepair(r)
		}
	}()
	s.emitRepair(r)
	p, err := repairProjectWithProbe(ctx, path, s.deps.Probe)
	if err != nil {
		return err
	}
	retained, err := analyzeSnapshot(ctx, p, "", response.reportID, "repair", 0, s.analysisOwner.Dir(), s.deps.Analyze, func(progress analysis.Progress) {
		s.mu.Lock()
		r.state.Phase = progress.Phase
		r.state.Current = progress.Current
		r.state.Total = progress.Total
		s.mu.Unlock()
		s.emitRepair(r)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if err = ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	r.report = retained
	r.state.Status = "awaitingDecision"
	r.state.Phase = "decision"
	s.mu.Unlock()
	s.emitRepair(r)
	select {
	case <-ctx.Done():
		s.mu.Lock()
		failure := r.failure
		s.mu.Unlock()
		if failure != nil {
			return failure
		}
		return ctx.Err()
	case proceed := <-r.decision:
		if !proceed {
			return context.Canceled
		}
	}
	if err = s.stopRepairPreview(r); err != nil {
		return err
	}
	s.mu.Lock()
	err = releaseAnalysisMedia(s.media, r.report)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.emitRepair(r)
	if err = analysis.CheckSources(ctx, retained.input, retained.result.Report.Sources); err != nil {
		return err
	}
	return ctx.Err()
}
func (s *MergerService) emitRepair(r *repairAnalysis) {
	s.repairEmitMu.Lock()
	defer s.repairEmitMu.Unlock()
	s.mu.Lock()
	state := repairState(r)
	s.mu.Unlock()
	s.deps.Emit("repair-analysis", state)
}
func repairState(r *repairAnalysis) RepairAnalysisState {
	state := r.state
	if r.report != nil {
		result := cloneAnalysisResult(r.report.result)
		state.Result = &result
	}
	return state
}
func (s *MergerService) GetRepairAnalysis(reportID string) (RepairAnalysisState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.repair == nil || s.repair.state.ReportID != reportID {
		return RepairAnalysisState{}, fmt.Errorf("unknown repair report")
	}
	return repairState(s.repair), nil
}
func (s *MergerService) ConfirmRepairAnalysis(reportID string, proceed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.repair
	if s.closed || r == nil || r.state.ReportID != reportID {
		return fmt.Errorf("unknown repair report")
	}
	if r.chosen != nil {
		if *r.chosen == proceed {
			return nil
		}
		return fmt.Errorf("conflicting repair decision")
	}
	if r.state.Status != "awaitingDecision" || r.ctx.Err() != nil {
		return fmt.Errorf("repair report is not awaiting a decision")
	}
	r.chosen = new(bool)
	*r.chosen = proceed
	r.state.Status = "cancelled"
	if proceed {
		r.state.Status = "proceeding"
	}
	if r.preview != nil {
		r.preview.cancel()
	}
	r.decision <- proceed
	return nil
}
func (s *MergerService) PreviewRepairFinding(reportID, findingID string, occurrence int) (result AudioFindingPreview, err error) {
	s.mu.Lock()
	r := s.repair
	if s.closed || r == nil || r.state.ReportID != reportID || r.state.Status != "awaitingDecision" || r.ctx.Err() != nil || r.preview != nil {
		s.mu.Unlock()
		return result, fmt.Errorf("repair audition is unavailable or already running")
	}
	ctx, cancel := context.WithCancel(r.ctx)
	job := &repairPreview{cancel: cancel, done: make(chan struct{})}
	r.preview = job
	s.previewWorkers.Add(1)
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		var cleanup *analysisPreviewCleanupError
		if errors.As(err, &cleanup) {
			job.cleanupErr = cleanup.error
		}
		// Retain failed cleanup even when the audition finished before proceed
		// begins its join. A subsequent audition cannot replace that failure.
		if r.preview == job && job.cleanupErr == nil {
			r.preview = nil
		}
		s.mu.Unlock()
		close(job.done)
		s.previewWorkers.Done()
	}()
	result, err = renderAnalysisPreview(ctx, r.report, uuid.NewString(), findingID, occurrence, s.cacheDir, s.media, s.deps.Preview)
	if err != nil {
		var changed *analysisSourceError
		if errors.As(err, &changed) {
			s.mu.Lock()
			if s.repair == r && r.state.Status == "awaitingDecision" {
				err = errors.Join(err, releaseAnalysisMedia(s.media, r.report))
				r.failure = err
				r.state.Status = "failed"
				r.state.Error = err.Error()
				if s.cfg != nil {
					s.cfg.cancel()
				}
			}
			s.mu.Unlock()
			s.emitRepair(r)
		}
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err == nil && (s.closed || s.repair != r || r.state.Status != "awaitingDecision") {
		err = context.Canceled
	}
	if err != nil {
		return AudioFindingPreview{}, errors.Join(err, previewCleanupFailure(s.media.Release(result.MediaID)))
	}
	r.report.media[result.MediaID] = true
	return result, nil
}
func (s *MergerService) ReleaseRepairMedia(reportID, mediaID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repair == nil || s.repair.state.ReportID != reportID || s.repair.report == nil {
		return fmt.Errorf("unknown repair report")
	}
	if !s.repair.report.media[mediaID] {
		return fmt.Errorf("media does not belong to repair report")
	}
	delete(s.repair.report.media, mediaID)
	return s.media.Release(mediaID)
}
func (s *MergerService) stopRepairPreview(r *repairAnalysis) error {
	s.mu.Lock()
	job := r.preview
	if job != nil {
		job.cancel()
	}
	s.mu.Unlock()
	if job != nil {
		<-job.done
		return job.cleanupErr
	}
	return nil
}
