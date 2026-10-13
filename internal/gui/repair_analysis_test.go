package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/app"
	"chaptersmith/internal/book"
	"chaptersmith/internal/store"
)

type repairFixture struct {
	s       *MergerService
	b       *BookService
	path    string
	prompt  chan string
	states  chan RepairAnalysisState
	done    chan error
	encoded atomic.Int32
}

func newRepairFixture(t *testing.T) *repairFixture {
	t.Helper()
	b, db, _ := newBookFixture(t)
	s := NewMergerServiceWithAnalysis(db, b.Media, b.cacheDir)
	t.Cleanup(func() { s.ServiceShutdown() })
	f := &repairFixture{s: s, b: b, path: analysisWAV(t, t.TempDir()), prompt: make(chan string, 1), states: make(chan RepairAnalysisState, 32), done: make(chan error, 1)}
	s.deps.Emit = func(name string, p any) {
		switch name {
		case "task-prompt-fix":
			f.prompt <- p.(map[string]any)["promptId"].(string)
		case "repair-analysis":
			f.states <- p.(RepairAnalysisState)
		}
	}
	s.deps.Run = func(ctx context.Context, cfg app.Config) error {
		choice := cfg.Progress.OnPromptFix("audio.wav")
		var err error
		if choice == "fix" {
			err = cfg.Progress.OnBeforeFix(ctx, f.path)
			if err == nil {
				f.encoded.Add(1)
			}
		} else if choice == "stop" {
			err = context.Canceled
		}
		f.done <- err
		return err
	}
	return f
}
func (f *repairFixture) start(t *testing.T, analyze bool) string {
	t.Helper()
	if err := f.s.StartMerge(filepath.Dir(f.path), filepath.Join(filepath.Dir(f.path), "out.m4b")); err != nil {
		t.Fatal(err)
	}
	id := <-f.prompt
	r, err := f.s.RespondFixPromptOptions(id, "fix", analyze)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *repairFixture) awaiting(t *testing.T) RepairAnalysisState {
	t.Helper()
	for {
		select {
		case st := <-f.states:
			if st.Status == "awaitingDecision" {
				return st
			}
			if st.Status == "failed" {
				t.Fatal(st.Error)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no report")
		}
	}
}
func TestRepairUncheckedCompatibility(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchecked", true: "legacy"}[legacy], func(t *testing.T) {
			f := newRepairFixture(t)
			f.s.deps.Analyze = func(context.Context, analysis.Input, analysis.Options, func(analysis.Progress)) (analysis.Report, error) {
				t.Error("unchecked analysis")
				return analysis.Report{}, nil
			}
			if legacy {
				f.s.StartMerge(filepath.Dir(f.path), "out.m4b")
				<-f.prompt
				f.s.RespondFixPrompt("fix")
			} else if f.start(t, false) != "" {
				t.Fatal("unchecked report ID")
			}
			if err := <-f.done; err != nil || f.encoded.Load() != 1 {
				t.Fatalf("legacy repair blocked %v", err)
			}
		})
	}
}
func TestRepairReportGate(t *testing.T) {
	for _, proceed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "proceed"}[proceed], func(t *testing.T) {
			f := newRepairFixture(t)
			id := f.start(t, true)
			st := f.awaiting(t)
			if st.ReportID != id || st.Result == nil || f.encoded.Load() != 0 {
				t.Fatal("gate not held")
			}
			if err := f.s.ConfirmRepairAnalysis(id, proceed); err != nil {
				t.Fatal(err)
			}
			err := <-f.done
			if proceed && (err != nil || f.encoded.Load() != 1) {
				t.Fatalf("proceed %v", err)
			}
			if !proceed && (!errors.Is(err, context.Canceled) || f.encoded.Load() != 0) {
				t.Fatalf("cancel %v", err)
			}
		})
	}
}
func TestRepairDecisionRaces(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			f := newRepairFixture(t)
			id := f.start(t, true)
			f.awaiting(t)
			if err := f.s.ConfirmRepairAnalysis("stale", true); err == nil {
				t.Fatal("stale accepted")
			}
			start := make(chan struct{})
			type decision struct {
				proceed bool
				err     error
			}
			results := make(chan decision, 2)
			for _, proceed := range []bool{true, false} {
				go func() { <-start; results <- decision{proceed, f.s.ConfirmRepairAnalysis(id, proceed)} }()
			}
			close(start)
			first, second := <-results, <-results
			if (first.err == nil) == (second.err == nil) {
				t.Fatalf("expected exactly one decision: %+v %+v", first, second)
			}
			accepted := first
			if accepted.err != nil {
				accepted = second
			}
			if err := f.s.ConfirmRepairAnalysis(id, accepted.proceed); err != nil {
				t.Fatal("duplicate not idempotent", err)
			}
			if err := f.s.ConfirmRepairAnalysis(id, !accepted.proceed); err == nil {
				t.Fatal("conflict accepted")
			}
			err := <-f.done
			if accepted.proceed {
				if err != nil || f.encoded.Load() != 1 {
					t.Fatalf("proceed encoded %d times: %v", f.encoded.Load(), err)
				}
			} else if !errors.Is(err, context.Canceled) || f.encoded.Load() != 0 {
				t.Fatalf("cancel encoded %d times: %v", f.encoded.Load(), err)
			}
		})
	}
}

func TestRepairCancelWaits(t *testing.T) {
	for _, phase := range []string{"prompt", "decision"} {
		t.Run(phase, func(t *testing.T) {
			f := newRepairFixture(t)
			if phase == "prompt" {
				f.s.StartMerge(filepath.Dir(f.path), "out.m4b")
				<-f.prompt
			} else {
				f.start(t, true)
				f.awaiting(t)
			}
			f.s.CancelMerge()
			select {
			case err := <-f.done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel left wait blocked")
			}
		})
	}
}
func TestRepairHistoryInsertFailure(t *testing.T) {
	f := newRepairFixture(t)
	f.s.store.Close()
	if err := f.s.StartMerge("input", "output"); err == nil {
		t.Fatal("closed store accepted")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "next.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f.s.store = db
	f.start(t, false)
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
}
func TestRepairEarlyStateRecovery(t *testing.T) {
	f := newRepairFixture(t)
	if err := f.s.StartMerge(filepath.Dir(f.path), "out.m4b"); err != nil {
		t.Fatal(err)
	}
	prompt := <-f.prompt
	// Delay delivery of the RPC response to model an event arriving at the UI first.
	release := make(chan struct{})
	response := make(chan string, 1)
	go func() {
		id, err := f.s.RespondFixPromptOptions(prompt, "fix", true)
		if err != nil {
			t.Error(err)
		}
		<-release
		response <- id
	}()
	early := f.awaiting(t)
	select {
	case <-response:
		t.Fatal("response was not delayed")
	default:
	}
	close(release)
	id := <-response
	st, err := f.s.GetRepairAnalysis(id)
	if err != nil || st.Status != "awaitingDecision" || st.Result == nil || early.ReportID != id {
		t.Fatalf("state lost %+v %v", st, err)
	}
	f.s.CancelMerge()
	<-f.done
}

func TestAnalysisMediaOwnership(t *testing.T) {
	f := newRepairFixture(t)
	id := f.start(t, true)
	f.awaiting(t)
	a, b := int64(100000), int64(200000)
	f.s.mu.Lock()
	f.s.repair.report.result.Report.Findings = []analysis.Finding{{ID: "located", StartUS: &a, EndUS: &b}}
	f.s.mu.Unlock()
	started, cleanup := make(chan struct{}), make(chan struct{})
	var path string
	f.s.deps.Preview = func(ctx context.Context, _ book.BookProject, _, dir string) (string, error) {
		path = renderedPreview(t, dir)
		close(started)
		<-ctx.Done()
		<-cleanup
		return path, nil
	}
	preview := make(chan error, 1)
	go func() { _, err := f.s.PreviewRepairFinding(id, "located", 0); preview <- err }()
	<-started
	if err := f.s.ConfirmRepairAnalysis(id, true); err != nil {
		t.Fatal(err)
	}
	if f.encoded.Load() != 0 {
		t.Fatal("encoder ran before preview joined")
	}
	close(cleanup)
	if err := <-preview; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("preview leaked")
	}
	if _, err := os.Stat(f.path); err != nil {
		t.Fatal("source deleted")
	}
}

// A source hash change must stop repair even with identical file size and timestamp.
func TestRepairProceedSourceHash(t *testing.T) {
	f := newRepairFixture(t)
	id := f.start(t, true)
	f.awaiting(t)
	st, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(f.path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteAt([]byte{1}, 50)
	file.Close()
	os.Chtimes(f.path, st.ModTime(), st.ModTime())
	if err = f.s.ConfirmRepairAnalysis(id, true); err != nil {
		t.Fatal(err)
	}
	if err = <-f.done; err == nil || f.encoded.Load() != 0 {
		t.Fatal("changed source reached repair")
	}
	state, err := f.s.GetRepairAnalysis(id)
	if err != nil || state.Status != "failed" {
		t.Fatalf("source invalidation state %+v %v", state, err)
	}
}
func TestRepairAnalysisFailureAndPartial(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "failed"}[failure], func(t *testing.T) {
			f := newRepairFixture(t)
			engine := f.s.deps.Analyze
			f.s.deps.Analyze = func(ctx context.Context, in analysis.Input, opts analysis.Options, progress func(analysis.Progress)) (analysis.Report, error) {
				if failure {
					return analysis.Report{}, errors.New("analysis storage failed")
				}
				r, err := engine(ctx, in, opts, progress)
				r.Coverage = []analysis.Coverage{{Detector: "decode", Complete: false, ScannedUS: 500000}}
				r.Limitations = []string{"partial media scan"}
				return r, err
			}
			id := f.start(t, true)
			if failure {
				if err := <-f.done; err == nil || f.encoded.Load() != 0 {
					t.Fatal("failed analysis encoded")
				}
				st, _ := f.s.GetRepairAnalysis(id)
				if st.Status != "failed" || st.Result != nil {
					t.Fatal(st)
				}
				return
			}
			st := f.awaiting(t)
			if st.Result.Report.Coverage[0].Complete {
				t.Fatal("partial lost")
			}
			f.s.ConfirmRepairAnalysis(id, true)
			if err := <-f.done; err != nil || f.encoded.Load() != 1 {
				t.Fatal("partial scan blocked explicit proceed", err)
			}
		})
	}
}

func TestRepairEncoderFailureState(t *testing.T) {
	f := newRepairFixture(t)
	run := f.s.deps.Run
	f.s.deps.Run = func(ctx context.Context, cfg app.Config) error {
		if err := run(ctx, cfg); err != nil {
			return err
		}
		return errors.New("encoder failed")
	}
	id := f.start(t, true)
	f.awaiting(t)
	f.s.ConfirmRepairAnalysis(id, true)
	<-f.done
	f.s.workers.Wait()
	st, _ := f.s.GetRepairAnalysis(id)
	if st.Status != "failed" || st.Error != "encoder failed" {
		t.Fatalf("operational failure not retained %+v", st)
	}
}

func TestRepairPreviewSourceInvalidatesDecision(t *testing.T) {
	f := newRepairFixture(t)
	id := f.start(t, true)
	f.awaiting(t)
	st, _ := os.Stat(f.path)
	file, err := os.OpenFile(f.path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteAt([]byte{1}, 50)
	file.Close()
	os.Chtimes(f.path, st.ModTime(), st.ModTime())
	if _, err = f.s.PreviewRepairFinding(id, "unknown", 0); err == nil {
		t.Fatal("changed source preview accepted")
	}
	if err = f.s.ConfirmRepairAnalysis(id, true); err == nil {
		t.Fatal("source-invalidated report accepted confirmation")
	}
	select {
	case <-f.done:
	case <-time.After(time.Second):
		t.Fatal("invalid report left job waiting")
	}
	state, _ := f.s.GetRepairAnalysis(id)
	if state.Status != "failed" || f.encoded.Load() != 0 {
		t.Fatalf("invalid report status %+v", state)
	}
}

// App's legacy stop branch returns an ordinary error; cancellation still owns the terminal state.
func TestRepairCancelledPromptTerminalState(t *testing.T) {
	f := newRepairFixture(t)
	f.s.deps.Run = func(ctx context.Context, cfg app.Config) error {
		cfg.Progress.OnPromptFix("audio.wav")
		<-ctx.Done()
		return errors.New("aborted: user chose to stop (single file, no fix)")
	}
	id := f.start(t, true)
	f.s.CancelMerge()
	f.s.workers.Wait()
	state, err := f.s.GetRepairAnalysis(id)
	if err != nil || state.Status != "cancelled" {
		t.Fatalf("cancelled prompt reported as failure: %+v %v", state, err)
	}
}

// A genuine nonempty owned directory makes Release fail after the late preview
// has been registered, without substituting a mock disposal function.
func TestRepairPreviewCleanupGate(t *testing.T) {
	for _, scenario := range []string{"clean_cancel", "cleanup_failure", "unavailable", "completed_cleanup_failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRepairFixture(t)
			id := f.start(t, true)
			f.awaiting(t)
			a, b := int64(100000), int64(200000)
			f.s.mu.Lock()
			f.s.repair.report.result.Report.Findings = []analysis.Finding{{ID: "located", StartUS: &a, EndUS: &b}}
			f.s.mu.Unlock()
			started := make(chan struct{})
			f.s.deps.Preview = func(ctx context.Context, _ book.BookProject, _, dir string) (string, error) {
				if scenario == "unavailable" {
					close(started)
					return "", errors.New("decoder cannot audition damaged interval")
				}
				path := renderedPreview(t, dir)
				if scenario == "cleanup_failure" || scenario == "completed_cleanup_failure" {
					if err := os.WriteFile(filepath.Join(filepath.Dir(path), "keep"), []byte("foreign sibling"), 0600); err != nil {
						t.Error(err)
					}
				}
				if scenario == "completed_cleanup_failure" {
					if err := f.s.media.Close(); err != nil {
						t.Error(err)
					}
					close(started)
					return path, nil
				}
				close(started)
				<-ctx.Done()
				return path, nil
			}
			audition := make(chan error, 1)
			go func() { _, err := f.s.PreviewRepairFinding(id, "located", 0); audition <- err }()
			<-started
			var previewErr error
			completed := scenario == "unavailable" || scenario == "completed_cleanup_failure"
			if completed {
				previewErr = <-audition
			}
			if err := f.s.ConfirmRepairAnalysis(id, true); err != nil {
				t.Fatal(err)
			}
			if !completed {
				previewErr = <-audition
			}
			repairErr := <-f.done
			failure := scenario == "cleanup_failure" || scenario == "completed_cleanup_failure"
			if failure {
				if previewErr == nil || !strings.Contains(previewErr.Error(), "removing owned media") {
					t.Fatalf("missing real disposal error: %v", previewErr)
				}
				if repairErr == nil || !strings.Contains(repairErr.Error(), "removing owned media") || f.encoded.Load() != 0 {
					t.Fatalf("cleanup failure allowed encoding (%d): %v", f.encoded.Load(), repairErr)
				}
				state, _ := f.s.GetRepairAnalysis(id)
				if state.Status != "failed" {
					t.Fatalf("cleanup failure status %+v", state)
				}
			} else if repairErr != nil || f.encoded.Load() != 1 {
				t.Fatalf("ordinary audition failure blocked repair (%d): %v", f.encoded.Load(), repairErr)
			}
			if scenario == "clean_cancel" && !errors.Is(previewErr, context.Canceled) {
				t.Fatal(previewErr)
			}
		})
	}
}

func TestRepairProceedCancelMergeRace(t *testing.T) {
	f := newRepairFixture(t)
	id := f.start(t, true)
	f.awaiting(t)
	a, b := int64(100000), int64(200000)
	f.s.mu.Lock()
	f.s.repair.report.result.Report.Findings = []analysis.Finding{{ID: "located", StartUS: &a, EndUS: &b}}
	f.s.mu.Unlock()
	started, cleanup := make(chan struct{}), make(chan struct{})
	f.s.deps.Preview = func(ctx context.Context, _ book.BookProject, _, dir string) (string, error) {
		path := renderedPreview(t, dir)
		close(started)
		<-ctx.Done()
		<-cleanup
		return path, nil
	}
	audition := make(chan error, 1)
	go func() { _, err := f.s.PreviewRepairFinding(id, "located", 0); audition <- err }()
	<-started
	start := make(chan struct{})
	confirmed := make(chan error, 1)
	cancelled := make(chan struct{})
	go func() { <-start; confirmed <- f.s.ConfirmRepairAnalysis(id, true) }()
	go func() { <-start; f.s.CancelMerge(); close(cancelled) }()
	close(start)
	confirmErr := <-confirmed
	<-cancelled
	if f.encoded.Load() != 0 {
		t.Fatal("encoder overtook preview cleanup")
	}
	close(cleanup)
	if err := <-audition; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-f.done; !errors.Is(err, context.Canceled) || f.encoded.Load() != 0 {
		t.Fatalf("cancel did not win before hook returned: count=%d %v", f.encoded.Load(), err)
	}
	if confirmErr == nil {
		if err := f.s.ConfirmRepairAnalysis(id, true); err != nil {
			t.Fatal("accepted decision lost idempotence", err)
		}
		if err := f.s.ConfirmRepairAnalysis(id, false); err == nil {
			t.Fatal("accepted decision allowed conflict")
		}
	}
}
