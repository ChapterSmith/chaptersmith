package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"chaptersmith/internal/app"
	"chaptersmith/internal/legal"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		outputFile    string
		playlistPath  string
		cuePath       string
		chapterSource string
		showVersion   bool
		fixMode       bool
	)

	flag.StringVar(&outputFile, "output", "", "output filename (default: directory name + .m4b)")
	flag.StringVar(&outputFile, "o", "", "output filename (shorthand)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&showVersion, "v", false, "print version (shorthand)")
	flag.BoolVar(&fixMode, "fix", false, "re-encode a single file preserving chapters and metadata")
	flag.StringVar(&playlistPath, "playlist", "", "explicit M3U/M3U8 playlist (preserves order and repeats)")
	flag.StringVar(&cuePath, "cue", "", "explicit CUE sheet (selects CUE chapters)")
	flag.StringVar(&chapterSource, "chapter-source", "auto", "chapter selection: auto, embedded, files or cue")
	flag.Parse()

	if showVersion {
		printVersion(os.Stdout)
		return nil
	}

	if outputFile == "" {
		if flag.NArg() > 0 {
			outputFile = flag.Arg(0)
		} else {
			currentDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			outputFile = filepath.Base(currentDir) + ".m4b"
		}
	}

	// Set up logging to both stdout and a log file.
	logFile, err := os.Create("chaptersmith.log")
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	defer logFile.Close()

	multiWriter := io.MultiWriter(os.Stdout, logFile)
	logger := log.New(multiWriter, "", log.LstdFlags)

	// Set up context with signal handling.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cpuCores := runtime.NumCPU()
	parallelism := max(1, int(float64(cpuCores)*1.25))

	cfg := app.Config{
		OutputPath:    outputFile,
		PlaylistPath:  playlistPath,
		CuePath:       cuePath,
		ChapterSource: chapterSource,
		CPUCores:      cpuCores,
		Parallelism:   parallelism,
		FixMode:       fixMode,
		Progress: &app.ProgressReporter{
			OnPromptFix: promptFixCLI,
		},
	}

	a := app.New(cfg, logger)
	return a.Run(ctx)
}

// promptFixCLI asks the user interactively whether to fix a single file,
// continue with the normal pipeline, or stop entirely.
func promptFixCLI(filename string) string {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("\nOnly 1 audio file found: %q\n", filename)
	fmt.Print("Re-encode and fix this file? (chapters and metadata will be preserved) [Y/n]: ")
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "" || answer == "y" || answer == "yes" {
		return "fix"
	}

	fmt.Print("Continue with normal merge anyway? [y/N]: ")
	answer, _ = reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "y" || answer == "yes" {
		return "continue"
	}

	fmt.Println("Stopping.")
	return "stop"
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "chaptersmith %s (built %s)\n", version, buildTime)
	fmt.Fprintln(w, legal.FFmpegNotice())
}
