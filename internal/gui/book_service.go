package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"chaptersmith/internal/analysis"
	"chaptersmith/internal/analysiscache"
	"chaptersmith/internal/book"
	"chaptersmith/internal/render"
	"chaptersmith/internal/store"
	"mime"
)

type bookDependencies struct {
	Analyze func(context.Context, analysis.Input, analysis.Options, func(analysis.Progress)) (analysis.Report, error)
	Export  func(context.Context, book.BookProject, string, *log.Logger, func(int, int, string)) (render.ExportResult, error)
	Preview func(context.Context, book.BookProject, string, string) (string, error)
	Pauses  func(context.Context, book.BookProject, float64, int64) ([]book.ChapterPause, error)
	Splits  func(context.Context, book.BookProject, book.SplitOptions, func(doneUS, totalUS int64)) ([]book.SplitProposal, error)
	Suggest func(context.Context, book.BookProject, book.SilenceOptions) ([]book.BoundaryProposal, error)
	Import  func(context.Context, book.ImportOptions) (book.BookProject, error)
	Pick    func(kind string) (string, error)
	Emit    func(name string, payload any)
}

// BookService owns one editing session and a single cancellable expensive operation.
type BookService struct {
	Media         *MediaRegistry `json:"-"`
	coverPath     string
	coverRef      MediaRef
	store         *store.DB
	cacheDir      string
	projectDir    string
	deps          bookDependencies
	mu            sync.Mutex
	emitMu        sync.Mutex
	project       book.BookProject
	revision      int64
	savedRevision int64
	undo          []book.BookProject
	sourceWarning string
	operation     *bookOperation
	operations    map[string]OperationStatus
	workers       sync.WaitGroup
	closed        bool
	audioReport   *retainedAnalysis
	analysisOwner *analysiscache.Owner
	shutdown      func() error
	shutdownOnce  sync.Once
	shutdownErr   error
}

func NewBookService(db *store.DB, cacheDir, projectDir string) (*BookService, error) {
	if db == nil {
		return nil, fmt.Errorf("book store is required")
	}
	if cacheDir == "" || projectDir == "" {
		return nil, fmt.Errorf("cache and project directories are required")
	}
	var err error
	cacheDir, err = filepath.Abs(cacheDir)
	if err != nil {
		return nil, err
	}
	projectDir, err = filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{cacheDir, filepath.Join(cacheDir, "covers"), filepath.Join(cacheDir, "preview"), projectDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("creating book directory: %w", err)
		}
	}
	s := &BookService{store: db, cacheDir: cacheDir, projectDir: projectDir, savedRevision: -1, operations: make(map[string]OperationStatus)}
	s.Media, err = NewMediaRegistry(MediaRoots{PreviewDir: filepath.Join(cacheDir, "preview"), CoverDirs: []string{filepath.Join(cacheDir, "covers"), projectDir}})
	if err != nil {
		return nil, err
	}
	s.analysisOwner, err = analysiscache.New(cacheDir)
	if err != nil {
		return nil, errors.Join(err, s.Media.Close())
	}
	s.deps = bookDependencies{Analyze: analysis.Analyze, Export: render.Export, Preview: render.Preview, Suggest: render.SuggestBoundaries, Splits: render.SuggestSplits, Pauses: render.MeasureChapterPauses, Import: book.Import, Pick: s.pickNative, Emit: func(name string, payload any) {
		if app := application.Get(); app != nil {
			app.Event.Emit(name, payload)
		}
	}}
	return s, nil
}

func (s *BookService) SelectBookInput(kind string) (string, error) {
	if kind != "folder" && kind != "file" {
		return "", fmt.Errorf("unknown book input kind %q", kind)
	}
	return s.pick(kind)
}
func (s *BookService) SelectSidecar(kind string) (string, error) {
	if kind != "playlist" && kind != "cue" {
		return "", fmt.Errorf("unknown sidecar kind %q", kind)
	}
	return s.pick(kind)
}
func (s *BookService) pick(kind string) (string, error) {
	s.mu.Lock()
	err := s.checkSwitchLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	path, err := s.deps.Pick(kind)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	err = s.checkSwitchLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return path, nil
}

func (s *BookService) LoadBook(requestID string, input LoadRequest) (view ProjectView, err error) {
	ctx, _, err := s.beginOperation(requestID, "load", 0)
	if err != nil {
		return ProjectView{}, err
	}
	defer func() {
		status, message := "succeeded", "Book loaded"
		if err != nil {
			status = "failed"
			message = err.Error()
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
			}
		}
		s.finishOperation(requestID, status, message)
	}()
	if input.InputPath == "" {
		return ProjectView{}, fmt.Errorf("inputPath is required")
	}
	source := input.ChapterSource
	if source == "" {
		source = "auto"
	}
	project, err := s.deps.Import(ctx, book.ImportOptions{InputPath: input.InputPath, CacheDir: filepath.Join(s.cacheDir, "covers"), PlaylistPath: input.PlaylistPath, CuePath: input.CuePath, ChapterSource: source})
	if err != nil {
		if ctx.Err() != nil {
			return ProjectView{}, ctx.Err()
		}
		return ProjectView{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectView{}, err
	}
	if err := book.Validate(project); err != nil {
		return ProjectView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ProjectView{}, err
	}
	if err := s.invalidateAudioLocked(); err != nil {
		return ProjectView{}, err
	}
	s.project = book.Clone(project)
	s.revision++
	s.savedRevision = -1
	s.undo = nil
	s.sourceWarning = ""
	s.operation.committed = true
	return s.viewLocked(), nil
}

func (s *BookService) OpenProject(projectID string) (ProjectView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkSwitchLocked(); err != nil {
		return ProjectView{}, err
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return ProjectView{}, err
	}
	warning := ""
	if err := book.CheckSources(project); err != nil {
		warning = err.Error()
	}
	if err := s.invalidateAudioLocked(); err != nil {
		return ProjectView{}, err
	}
	s.project = book.Clone(project)
	s.revision++
	s.savedRevision = s.revision
	s.undo = nil
	s.sourceWarning = warning
	return s.viewLocked(), nil
}
func (s *BookService) ListProjects(limit int) ([]store.ProjectSummary, error) {
	return s.store.ListProjects(limit)
}
func (s *BookService) GetProject() (ProjectView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.project.ID == "" {
		return ProjectView{}, fmt.Errorf("no project open")
	}
	return s.viewLocked(), nil
}
func (s *BookService) SaveProject(expectedRevision int64) (ProjectView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMutationLocked(expectedRevision); err != nil {
		return ProjectView{}, err
	}
	if err := s.store.SaveProject(s.project); err != nil {
		return ProjectView{}, err
	}
	durable, err := s.store.GetProject(s.project.ID)
	if err != nil {
		return ProjectView{}, err
	}
	s.project = book.Clone(durable)
	s.savedRevision = s.revision
	return s.viewLocked(), nil
}
func (s *BookService) ApplyEdit(expectedRevision int64, edit EditRequest) (ProjectView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMutationLocked(expectedRevision); err != nil {
		return ProjectView{}, err
	}
	candidate := book.Clone(s.project)
	var err error
	switch edit.Kind {
	case "metadata":
		if edit.Metadata == nil {
			return ProjectView{}, fmt.Errorf("metadata snapshot is required")
		}
		candidate.Metadata = *edit.Metadata
	case "export":
		if edit.Export == nil {
			return ProjectView{}, fmt.Errorf("export settings snapshot is required")
		}
		candidate.Export = *edit.Export
	case "cover-remove":
		candidate.CoverPath = ""
	case "rename":
		candidate, err = book.RenameChapter(candidate, edit.ChapterID, edit.Title)
	case "move":
		candidate, err = book.MoveChapter(candidate, edit.ChapterID, edit.Destination)
	case "boundary":
		candidate, err = book.ShiftBoundary(candidate, edit.ChapterID, edit.NewStartUS, edit.Mode)
	case "silence":
		candidate, err = book.AddSilence(candidate, edit.ChapterID, edit.DurationUS, edit.Position)
	case "split":
		candidate, err = book.SplitChapters(candidate, edit.PositionsUS)
	case "merge":
		candidate, err = book.MergeChapter(candidate, edit.ChapterID)
	case "pauses":
		candidate, err = book.SetChapterPauses(candidate, edit.Pauses, edit.DurationUS)
	default:
		return ProjectView{}, fmt.Errorf("unknown edit kind %q", edit.Kind)
	}
	if err != nil {
		return ProjectView{}, err
	}
	return s.commitEditLocked(candidate)
}
func (s *BookService) SelectCover(expectedRevision int64) (ProjectView, error) {
	s.mu.Lock()
	err := s.checkMutationLocked(expectedRevision)
	s.mu.Unlock()
	if err != nil {
		return ProjectView{}, err
	}
	path, err := s.deps.Pick("cover")
	if err != nil {
		return ProjectView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A cancelled dialog always returns the current unchanged view.
	if path == "" {
		return s.viewLocked(), nil
	}
	if err := s.checkMutationLocked(expectedRevision); err != nil {
		return ProjectView{}, err
	}
	cover, err := book.CacheCover(filepath.Join(s.cacheDir, "covers"), path)
	if err != nil {
		return ProjectView{}, err
	}
	candidate := book.Clone(s.project)
	candidate.CoverPath = cover
	return s.commitEditLocked(candidate)
}
func (s *BookService) Undo(expectedRevision int64) (ProjectView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkMutationLocked(expectedRevision); err != nil {
		return ProjectView{}, err
	}
	if len(s.undo) == 0 {
		return ProjectView{}, fmt.Errorf("nothing to undo")
	}
	candidate := book.Clone(s.undo[len(s.undo)-1])
	if err := book.Validate(candidate); err != nil {
		return ProjectView{}, err
	}
	if err := s.invalidateAudioLocked(); err != nil {
		return ProjectView{}, err
	}
	s.undo = s.undo[:len(s.undo)-1]
	s.project = candidate
	s.revision++
	return s.viewLocked(), nil
}
func (s *BookService) commitEditLocked(candidate book.BookProject) (ProjectView, error) {
	if err := book.Validate(candidate); err != nil {
		return ProjectView{}, err
	}
	if err := s.invalidateAudioLocked(); err != nil {
		return ProjectView{}, err
	}
	s.undo = append(s.undo, book.Clone(s.project))
	if len(s.undo) > 50 {
		copy(s.undo, s.undo[1:])
		s.undo[len(s.undo)-1] = book.BookProject{}
		s.undo = s.undo[:50]
	}
	s.project = book.Clone(candidate)
	s.revision++
	return s.viewLocked(), nil
}
func (s *BookService) checkRevisionLocked(expectedRevision int64) error {
	if s.closed {
		return fmt.Errorf("book service is closed")
	}
	if s.project.ID == "" {
		return fmt.Errorf("no project open")
	}
	if expectedRevision != s.revision {
		return fmt.Errorf("stale revision")
	}
	return nil
}
func (s *BookService) checkMutationLocked(expectedRevision int64) error {
	if err := s.checkRevisionLocked(expectedRevision); err != nil {
		return err
	}
	if s.operation != nil && s.operations[s.operation.requestID].Kind != "export" {
		return fmt.Errorf("operation already running")
	}
	return nil
}
func (s *BookService) checkSwitchLocked() error {
	if s.closed {
		return fmt.Errorf("book service is closed")
	}
	if s.operation != nil {
		return fmt.Errorf("operation already running")
	}
	return nil
}
func (s *BookService) viewLocked() ProjectView {
	project := book.Clone(s.project)
	rows := make([]ChapterRow, 0, len(project.Chapters))
	var start int64
	for _, chapter := range project.Chapters {
		var duration int64
		for _, segment := range chapter.Segments {
			if segment.SourceID == "" {
				duration += segment.SilenceUS
			} else {
				duration += segment.EndUS - segment.StartUS
			}
		}
		rows = append(rows, ChapterRow{ID: chapter.ID, Title: chapter.Title, StartUS: start, DurationUS: duration})
		start += duration
	}
	if project.CoverPath != s.coverPath {
		_ = s.Media.Release(s.coverRef.ID)
		s.coverPath, s.coverRef = "", MediaRef{}
		if project.CoverPath != "" {
			contentType := mime.TypeByExtension(filepath.Ext(project.CoverPath))
			if ref, err := s.Media.Register(project.CoverPath, contentType); err == nil {
				s.coverPath, s.coverRef = project.CoverPath, ref
			}
		}
	}
	return ProjectView{Project: project, Revision: s.revision, Dirty: s.revision != s.savedRevision, CanUndo: len(s.undo) > 0, DurationUS: book.DurationUS(project), ChapterRows: rows, CoverURL: s.coverRef.URL, SourceWarning: s.sourceWarning}
}

// The native picker never runs while holding the session mutex.
func (s *BookService) pickNative(kind string) (string, error) {
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("native dialog is unavailable")
	}
	if strings.HasPrefix(kind, "export:") {
		format := strings.TrimPrefix(kind, "export:")
		s.mu.Lock()
		name := suggestedExportName(s.project.Metadata.Title, s.project.Metadata.Artist)
		s.mu.Unlock()
		return app.Dialog.SaveFile().SetFilename(name+"."+format).AddFilter("Audiobook", "*."+format).PromptForSingleSelection()
	}
	dialog := app.Dialog.OpenFile().CanChooseDirectories(false).CanChooseFiles(true)
	switch kind {
	case "folder":
		dialog.SetTitle("Select Book Folder").CanChooseDirectories(true).CanChooseFiles(false)
	case "file":
		dialog.SetTitle("Select Audio File").AddFilter("Audio", "*.m4b;*.m4a;*.mp3;*.aac;*.ogg;*.flac;*.wav;*.opus")
	case "playlist":
		dialog.SetTitle("Select Playlist").AddFilter("Playlist", "*.m3u;*.m3u8")
	case "cue":
		dialog.SetTitle("Select CUE Sheet").AddFilter("CUE Sheet", "*.cue")
	case "cover":
		dialog.SetTitle("Select Cover").AddFilter("Cover Image", "*.jpg;*.jpeg;*.png")
		s.mu.Lock()
		directory := ""
		if len(s.project.Sources) > 0 {
			directory = filepath.Dir(s.project.Sources[0].Path)
		}
		s.mu.Unlock()
		if directory != "" {
			dialog.SetDirectory(directory)
		}
	default:
		return "", fmt.Errorf("unknown picker kind %q", kind)
	}
	return dialog.PromptForSingleSelection()
}
