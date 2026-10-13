package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"chaptersmith/internal/meta"
	"chaptersmith/internal/muxer"
)

// ProgressReporter receives progress and log updates during a merge.
// All callbacks are optional; nil is safe.
type ProgressReporter struct {
	OnPhase    func(phase string)
	OnProgress func(current, total int, message string)
	OnLog      func(line string)

	// OnPromptFix is called when only one audio file is found and FixMode
	// is not pre-set. It receives the filename and must return one of:
	//   "fix"      - re-encode the file preserving chapters and metadata
	//   "stop"     - abort the operation
	//   "continue" - proceed with the normal merge pipeline
	OnPromptFix func(filename string) string

	// OnBeforeFix gates repair before any metadata, artwork or encoding work.
	OnBeforeFix func(context.Context, string) error
}

// Config holds all configuration for the application.
type Config struct {
	PlaylistPath  string // explicit playlist selection
	CuePath       string // explicit CUE selection
	ChapterSource string // auto, embedded, files or cue
	InputDir      string // working directory containing audio files; if empty, use current dir
	OutputPath    string // full path to output .m4b file (or filename for CLI; CLI sets this)
	CPUCores      int
	Parallelism   int
	Progress      *ProgressReporter
	FixMode       bool // when true, skip the prompt and go straight to fix pipeline
}

// App holds application state and configuration.
type App struct {
	cfg    Config
	logger *log.Logger
}

// New creates a new App instance.
func New(cfg Config, logger *log.Logger) *App {
	return &App{
		cfg:    cfg,
		logger: logger,
	}
}

// logWriter bridges log output to the OnLog callback.
type logWriter struct {
	onLog func(string)
	buf   bytes.Buffer
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	n, err = w.buf.Write(p)
	if err != nil {
		return n, err
	}
	for {
		end := bytes.IndexByte(w.buf.Bytes(), '\n')
		if end < 0 {
			break
		}
		line := string(bytes.TrimRight(w.buf.Next(end+1), "\r\n"))
		if w.onLog != nil {
			w.onLog(line)
		}
	}
	return n, nil
}

// isGUI returns true when the app is running inside the Wails GUI
// (which sets OnLog to bridge log output to the frontend).
func (a *App) isGUI() bool {
	return a.cfg.Progress != nil && a.cfg.Progress.OnLog != nil
}

func (a *App) emitPhase(phase string) {
	a.logger.Printf("\n=== %s ===", phase)
	if a.cfg.Progress != nil && a.cfg.Progress.OnPhase != nil {
		a.cfg.Progress.OnPhase(phase)
	}
}

func (a *App) emitProgress(current, total int, message string) {
	if a.cfg.Progress != nil && a.cfg.Progress.OnProgress != nil {
		a.cfg.Progress.OnProgress(current, total, message)
	}
}

// Run executes the full audiobook merge pipeline.
func (a *App) Run(ctx context.Context) error {
	workDir := "."
	if a.cfg.InputDir != "" {
		workDir = a.cfg.InputDir
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("resolving work directory: %w", err)
	}
	workDir = absWorkDir

	outputFile := a.cfg.OutputPath
	if outputFile == "" {
		outputFile = filepath.Join(workDir, filepath.Base(workDir)+".m4b")
	} else if !filepath.IsAbs(outputFile) {
		outputFile = filepath.Join(workDir, outputFile)
	}

	excludeFile := outputFile

	// Set up logging to both logger and OnLog callback.
	var logDest io.Writer = a.logger.Writer()
	if a.cfg.Progress != nil && a.cfg.Progress.OnLog != nil {
		multiLog := &logWriter{onLog: a.cfg.Progress.OnLog}
		logDest = io.MultiWriter(logDest, multiLog)
	}
	runLogger := log.New(logDest, "", log.LstdFlags)

	runLogger.Printf("=== ChapterSmith ===")
	runLogger.Printf("Started: %s", time.Now().Format(time.RFC1123))
	runLogger.Printf("CPU cores detected: %d (using %d parallel jobs)", a.cfg.CPUCores, a.cfg.Parallelism)
	runLogger.Printf("Output file will be: %s", outputFile)

	a.emitPhase("DISCOVERY")
	inputs, err := book.DiscoverInputs(book.ImportOptions{InputPath: workDir, PlaylistPath: a.cfg.PlaylistPath, CuePath: a.cfg.CuePath, ChapterSource: a.cfg.ChapterSource}, excludeFile)
	if err != nil {
		return fmt.Errorf("finding audio files: %w", err)
	}
	workDir = inputs.WorkDir
	audioFiles := inputs.Files
	runLogger.Printf("Found %d audio file(s)", len(audioFiles))
	a.emitProgress(len(audioFiles), len(audioFiles), "Discovery complete")

	// --- Single-file detection: prompt for fix / stop / continue ---
	if len(audioFiles) == 1 {
		doFix := a.cfg.FixMode
		if !doFix {
			choice := "continue"
			if a.cfg.Progress != nil && a.cfg.Progress.OnPromptFix != nil {
				choice = a.cfg.Progress.OnPromptFix(filepath.Base(audioFiles[0]))
			}
			switch choice {
			case "fix":
				doFix = true
			case "stop":
				runLogger.Printf("User chose to stop.")
				return fmt.Errorf("aborted: user chose to stop (single file, no fix)")
			default:
				// "continue" -- fall through to normal pipeline
			}
		}
		if doFix {
			// Ensure output differs from input.
			absInput, _ := filepath.Abs(audioFiles[0])
			absOutput, err := outputAbsoluteName(outputFile)
			if err != nil {
				return err
			}
			if absOutput == absInput {
				ext := filepath.Ext(outputFile)
				outputFile = outputFile[:len(outputFile)-len(ext)] + "_fixed.m4b"
				runLogger.Printf("Output path equals input; changed to: %s", outputFile)
				// Automatic naming must not overwrite a previously existing destination.
				for _, destination := range []string{outputFile} {
					if _, err := os.Lstat(destination); err == nil {
						return fmt.Errorf("automatic fix output %s already exists", destination)
					} else if !os.IsNotExist(err) {
						return err
					}
				}
			}
			if a.cfg.Progress != nil && a.cfg.Progress.OnBeforeFix != nil {
				if err := a.cfg.Progress.OnBeforeFix(ctx, audioFiles[0]); err != nil {
					return err
				}
			}
			return a.runFixMode(ctx, audioFiles[0], outputFile, workDir, inputs, runLogger)
		}
	}

	// Protect selected sources before opening any output.
	if err := rejectInputOutputs(inputs, runLogger, outputFile); err != nil {
		return err
	}
	a.emitPhase("CHAPTER HANDLING")
	chapters, err := loadChapterMarkers(ctx, inputs, a.chapterMode())
	if err != nil {
		return fmt.Errorf("loading chapters: %w", err)
	}

	// --- Normal merge pipeline ---

	tempDir, err := os.MkdirTemp("", "chaptersmith-*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() {
		runLogger.Printf("\n=== CLEANUP ===")
		runLogger.Printf("Removing temporary files...")
		if err := os.RemoveAll(tempDir); err != nil {
			runLogger.Printf("Warning: failed to remove temp directory: %v", err)
		}
	}()

	a.emitPhase("METADATA COLLECTION")
	var metadata meta.AudiobookMetadata
	if a.isGUI() {
		metadata = meta.CollectMetadataFromDir(workDir, audioFiles[0], runLogger)
	} else {
		metadata = meta.CollectMetadata(workDir, audioFiles[0], runLogger)
	}

	a.emitPhase("COVER IMAGE HANDLING")
	coverFile, err := meta.CacheCoverImage(workDir, audioFiles[0], tempDir, runLogger)
	if err != nil {
		return fmt.Errorf("caching cover image: %w", err)
	}
	hasCover := coverFile != ""

	mergedFile := filepath.Join(tempDir, "merged.m4a")
	compatible, _ := audio.CheckFilesCompatible(audioFiles)
	if len(audioFiles) == 1 && compatible {
		a.emitPhase("AUDIO CONVERSION (SKIPPED)")
		runLogger.Printf("Single compatible AAC source -- skipping transcode")
		if err := audio.Concat(ctx, audioFiles, mergedFile, runLogger); err != nil {
			return fmt.Errorf("copying audio: %w", err)
		}
	} else {
		a.emitPhase("AUDIO CONVERSION")
		runLogger.Printf("Converting original occurrences with one continuous AAC encoder (128000 bit/s)")
		joined, err := audio.JoinAAC(ctx, audioFiles, mergedFile, func(current, total int) {
			a.emitProgress(current, total, fmt.Sprintf("Converting %d/%d", current, total))
		})
		if err != nil {
			return fmt.Errorf("converting audio files: %w", err)
		}
		chapters, err = loadJoinedChapterMarkers(ctx, inputs, a.chapterMode(), joined)
		if err != nil {
			return fmt.Errorf("reconciling chapters: %w", err)
		}
	}

	a.emitPhase("FINALIZING AUDIOBOOK")
	muxOpts := muxer.Options{
		InputFile:   mergedFile,
		OutputFile:  outputFile,
		Metadata:    metadata,
		CoverFile:   coverFile,
		HasCover:    hasCover,
		Chapters:    chapters,
		HasChapters: len(chapters) > 0,
	}
	if err := muxer.Finalize(ctx, muxOpts, runLogger); err != nil {
		return fmt.Errorf("finalizing audiobook: %w", err)
	}

	duration, _ := audio.GetDuration(outputFile)
	fileSize, _ := audio.GetFileSize(outputFile)

	runLogger.Printf("\nSUCCESS! Audiobook created: '%s'", outputFile)
	runLogger.Printf("File size: %s - Duration: %s", fileSize, audio.FormatDuration(duration))
	runLogger.Printf("Finished: %s", time.Now().Format(time.RFC1123))
	a.emitProgress(1, 1, "Complete")

	return nil
}

// runFixMode re-encodes a single audio file while preserving its embedded
// chapters, metadata, and cover art. This fixes files that "stop randomly"
// by producing a clean AAC stream in a new M4B container.
func (a *App) runFixMode(ctx context.Context, inputFile, outputFile, workDir string, inputs book.ImportInputs, runLogger *log.Logger) error {
	if err := rejectInputOutputs(inputs, runLogger, outputFile); err != nil {
		return err
	}
	a.emitPhase("READING CHAPTERS")
	chapters, err := loadChapterMarkers(ctx, inputs, a.chapterMode())
	if err != nil {
		return fmt.Errorf("loading chapters: %w", err)
	}

	runLogger.Printf("=== FIX MODE: converting to M4B audiobook ===")
	runLogger.Printf("Input:  %s", inputFile)
	runLogger.Printf("Output: %s", outputFile)

	tempDir, err := os.MkdirTemp("", "chaptersmith-fix-*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() {
		runLogger.Printf("\n=== CLEANUP ===")
		runLogger.Printf("Removing temporary files...")
		if err := os.RemoveAll(tempDir); err != nil {
			runLogger.Printf("Warning: failed to remove temp directory: %v", err)
		}
	}()

	a.emitPhase("SCANNING COMPANION FILES")
	meta.LogSidecars(meta.DiscoverSidecars(workDir), runLogger)

	a.emitPhase("READING METADATA")
	metadata := meta.CollectMetadataFromDir(workDir, inputFile, runLogger)

	a.emitPhase("COVER IMAGE HANDLING")
	coverFile, err := meta.CacheCoverImage(workDir, inputFile, tempDir, runLogger)
	if err != nil {
		return fmt.Errorf("caching cover image: %w", err)
	}
	hasCover := coverFile != ""

	// 4. Transcode source audio to AAC (required for M4B; MP3 and other formats are not kept).
	a.emitPhase("CONVERTING TO AAC")
	transcodedFile := filepath.Join(tempDir, "fixed.m4a")
	runLogger.Printf("Converting %s with one continuous AAC encoder (128000 bit/s)...", filepath.Base(inputFile))
	a.emitProgress(0, 1, "Converting to AAC...")
	joined, err := audio.JoinAAC(ctx, []string{inputFile}, transcodedFile, nil)
	if err != nil {
		return fmt.Errorf("converting to AAC: %w", err)
	}
	chapters, err = loadJoinedChapterMarkers(ctx, inputs, a.chapterMode(), joined)
	if err != nil {
		return fmt.Errorf("reconciling chapters: %w", err)
	}
	a.emitProgress(1, 1, "AAC conversion complete")

	// 5. Finalize: mux AAC audio + chapters + metadata + cover into M4B.
	a.emitPhase("FINALIZING M4B")
	muxOpts := muxer.Options{
		InputFile:   transcodedFile,
		OutputFile:  outputFile,
		Metadata:    metadata,
		CoverFile:   coverFile,
		HasCover:    hasCover,
		Chapters:    chapters,
		HasChapters: len(chapters) > 0,
	}
	if err := muxer.Finalize(ctx, muxOpts, runLogger); err != nil {
		return fmt.Errorf("finalizing audiobook: %w", err)
	}

	duration, _ := audio.GetDuration(outputFile)
	fileSize, _ := audio.GetFileSize(outputFile)

	runLogger.Printf("\nSUCCESS! Fixed audiobook created: '%s'", outputFile)
	runLogger.Printf("File size: %s - Duration: %s", fileSize, audio.FormatDuration(duration))
	runLogger.Printf("Finished: %s", time.Now().Format(time.RFC1123))
	a.emitProgress(1, 1, "Complete")

	return nil
}

// DiscoverInputs has already checked conflicts; an explicit CUE wins over auto.
func (a *App) chapterMode() string {
	if a.cfg.CuePath != "" {
		return "cue"
	}
	return strings.ToLower(a.cfg.ChapterSource)
}
