package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"chaptersmith/internal/analysiscache"
	"chaptersmith/internal/app"
	"chaptersmith/internal/legal"
	"chaptersmith/internal/store"
)

// MergerService exposes merge operations to the Wails frontend.
type MergerService struct {
	store             *store.DB
	mu                sync.Mutex
	cfg               *mergeConfig
	media             *MediaRegistry
	cacheDir          string
	deps              repairDependencies
	repair            *repairAnalysis
	repairEmitMu      sync.Mutex
	workers           sync.WaitGroup
	previewWorkers    sync.WaitGroup
	closed            bool
	analysisOwner     *analysiscache.Owner
	ownsAnalysisOwner bool
	shutdown          func() error
	shutdownOnce      sync.Once
	shutdownErr       error
}

type mergeConfig struct {
	ctx            context.Context
	cancel         context.CancelFunc
	taskID         int64
	fixResponse    chan fixResponse
	promptID       string
	promptAnswered bool
	response       fixResponse
}

// NewMergerService preserves the unchecked repair constructor.
func NewMergerService(db *store.DB) *MergerService { return NewMergerServiceWithAnalysis(db, nil, "") }

func (s *MergerService) app() *application.App {
	return application.Get()
}

// SelectInputFolder opens a directory picker and returns the selected path.
func (s *MergerService) SelectInputFolder() (string, error) {
	path, err := s.app().Dialog.OpenFile().
		SetTitle("Select Book Folder").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return path, nil
}

// SelectOutputFile opens a save dialog for the output .m4b file.
func (s *MergerService) SelectOutputFile(defaultName string) (string, error) {
	if defaultName == "" {
		defaultName = "audiobook.m4b"
	}
	if filepath.Ext(defaultName) != ".m4b" {
		defaultName = defaultName + ".m4b"
	}
	path, err := s.app().Dialog.SaveFile().
		SetFilename(defaultName).
		AddFilter("Audiobook", "*.m4b").
		AddFilter("All Files", "*.*").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return path, nil
}

// StartMerge begins the merge process in a goroutine.
func (s *MergerService) StartMerge(inputDir, outputPath string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("merger service is closed")
	}
	if s.cfg != nil {
		s.mu.Unlock()
		return errors.New("a merge is already running")
	}
	if s.repair != nil && s.repair.report != nil {
		if err := releaseAnalysisMedia(s.media, s.repair.report); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.repair = nil
	ctx, cancel := context.WithCancel(context.Background())
	job := &mergeConfig{ctx: ctx, cancel: cancel, fixResponse: make(chan fixResponse, 1)}
	s.cfg = job
	s.workers.Add(1)
	s.mu.Unlock()
	taskID, err := s.store.InsertTask(inputDir, outputPath)
	if err != nil {
		cancel()
		s.mu.Lock()
		if s.cfg == job {
			s.cfg = nil
		}
		s.mu.Unlock()
		defer s.workers.Done()
		s.emitDone(false, 0, "", err.Error())
		return err
	}
	s.mu.Lock()
	job.taskID = taskID
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		defer func() {
			cancel()
			s.mu.Lock()
			if s.cfg == job {
				s.cfg = nil
			}
			s.mu.Unlock()
		}()
		onLog := func(line string) {
			s.deps.Emit("task-log", map[string]any{"line": line})
			_ = s.store.AppendLog(taskID, line)
		}
		onPhase := func(phase string) {
			s.deps.Emit("task-progress", map[string]any{"phase": phase, "current": 0, "total": 1, "message": phase})
		}
		onProgress := func(current, total int, message string) {
			s.deps.Emit("task-progress", map[string]any{"phase": "progress", "current": current, "total": total, "message": message})
		}
		cpuCores := runtime.NumCPU()
		cfg := app.Config{InputDir: inputDir, OutputPath: outputPath, CPUCores: cpuCores, Parallelism: max(1, int(float64(cpuCores)*1.25)), Progress: &app.ProgressReporter{OnPhase: onPhase, OnProgress: onProgress, OnLog: onLog, OnPromptFix: func(filename string) string { return s.promptFix(ctx, job, filename) }, OnBeforeFix: func(ctx context.Context, path string) error { return s.beforeFix(ctx, job, path) }}}
		err := ctx.Err()
		if err == nil {
			err = s.deps.Run(ctx, cfg)
		}

		if ctx.Err() != nil {
			err = ctx.Err()
			s.mu.Lock()
			if s.repair != nil && s.repair.failure != nil {
				err = s.repair.failure
			}
			s.mu.Unlock()
		}
		// Cleanup remains inside the job slot and precedes its terminal event.
		cancel()
		s.mu.Lock()
		retained := s.repair
		s.mu.Unlock()
		if retained != nil {
			if cleanup := s.stopRepairPreview(retained); cleanup != nil && !errors.Is(err, cleanup) {
				err = errors.Join(err, cleanup)
			}
			s.mu.Lock()
			if retained.report != nil {
				err = errors.Join(err, releaseAnalysisMedia(s.media, retained.report))
			}
			changed := false
			if err != nil {
				status := "failed"
				if errors.Is(err, context.Canceled) {
					status = "cancelled"
				}
				changed = retained.state.Status != status || retained.state.Error != err.Error()
				retained.state.Status = status
				retained.state.Error = err.Error()
			}
			s.mu.Unlock()
			if changed {
				s.emitRepair(retained)
			}
		}
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
			log.Printf("Merge failed: %v", err)
			onLog("ERROR: " + errMsg)
		}
		_ = s.store.CompleteTask(taskID, err == nil, errMsg)
		s.mu.Lock()
		if s.cfg == job {
			s.cfg = nil
		}
		s.mu.Unlock()
		s.emitDone(err == nil, taskID, outputPath, errMsg)
	}()
	return nil
}

func (s *MergerService) emitDone(success bool, taskID int64, outputPath, errMsg string) {
	s.deps.Emit("task-done", map[string]any{
		"success":    success,
		"taskID":     taskID,
		"outputPath": outputPath,
		"error":      errMsg,
	})
}

// RespondFixPrompt sends the user's choice back to the blocked OnPromptFix
// callback. Valid values: "fix", "continue", "stop".
func (s *MergerService) RespondFixPrompt(choice string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg != nil {
		_, _ = s.respondFixLocked(s.cfg.promptID, choice, false)
	}
}

// CancelMerge cancels the currently running merge.
func (s *MergerService) CancelMerge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg != nil && s.cfg.cancel != nil {
		s.cfg.cancel()
	}
}

// GetHistory returns recent merge tasks.
func (s *MergerService) GetHistory(limit int) ([]store.TaskEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.store.GetHistory(limit)
}

// GetTaskLogs returns log lines for a task.
func (s *MergerService) GetTaskLogs(taskID int64) ([]store.LogLine, error) {
	return s.store.GetTaskLogs(taskID)
}

// LegalNotice returns the third-party license attribution shown in the UI.
func (s *MergerService) LegalNotice() string {
	return legal.FFmpegNotice()
}
