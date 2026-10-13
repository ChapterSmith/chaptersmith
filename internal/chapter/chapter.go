package chapter

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/asticode/go-astiav"

	"chaptersmith/internal/audio"
)

// Chapter represents a chapter marker with a title and start time.
type Chapter struct {
	Title     string
	StartTime float64 // seconds
}

// ResolveFixChapters loads chapter markers for fix mode from embedded tags
// or a CUE sheet in the book folder.
func ResolveFixChapters(workDir, inputFile string, logger *log.Logger) []Chapter {
	chapters, err := ReadEmbeddedChapters(inputFile)
	if err != nil {
		logger.Printf("Warning: could not read embedded chapters: %v", err)
	}
	if len(chapters) > 0 {
		logger.Printf("Found %d embedded chapters.", len(chapters))
		return chapters
	}

	logger.Printf("No embedded chapters found.")
	for _, pattern := range []string{"*.cue", "*.CUE"} {
		cueFiles, _ := filepath.Glob(filepath.Join(workDir, pattern))
		if len(cueFiles) == 0 {
			continue
		}
		logger.Printf("Found CUE file: %s", filepath.Base(cueFiles[0]))
		cueChapters, cueErr := ParseCueFile(cueFiles[0], logger)
		if cueErr != nil {
			logger.Printf("Warning: could not parse CUE file: %v", cueErr)
			continue
		}
		if len(cueChapters) > 0 {
			logger.Printf("Using %d chapters from CUE file.", len(cueChapters))
			return cueChapters
		}
	}

	return nil
}

// HandleChapters creates chapter markers from a CUE sheet or from individual
// audio file boundaries. Returns the list of chapters (empty if none found).
func HandleChapters(ctx context.Context, workDir string, audioFiles []string, m3uEntries []audio.M3UEntry, logger *log.Logger) []Chapter {
	cueFiles, _ := filepath.Glob(filepath.Join(workDir, "*.cue"))
	if len(cueFiles) > 0 {
		logger.Printf("Found CUE file: %s", cueFiles[0])
		chapters, err := ParseCueFile(cueFiles[0], logger)
		if err == nil && len(chapters) > 0 {
			logger.Printf("Successfully parsed %d chapters from CUE file.", len(chapters))
			return chapters
		}
		if err != nil {
			logger.Printf("CUE parsing failed: %v", err)
		}
	}

	logger.Printf("Creating chapters from individual audio files...")
	return createChaptersFromAudioFiles(ctx, audioFiles, m3uEntries, logger)
}

func createChaptersFromAudioFiles(ctx context.Context, audioFiles []string, m3uEntries []audio.M3UEntry, logger *log.Logger) []Chapter {
	if len(audioFiles) <= 1 {
		if len(audioFiles) == 1 {
			logger.Printf("Single file -- attempting to read embedded chapters...")
			chapters, err := ReadEmbeddedChapters(audioFiles[0])
			if err != nil {
				logger.Printf("Warning: could not read embedded chapters: %v", err)
			}
			if len(chapters) > 0 {
				logger.Printf("Found %d embedded chapters.", len(chapters))
				return chapters
			}
			logger.Printf("No embedded chapters found in single file.")
		}
		return nil
	}

	var chapters []Chapter
	var currentTime float64

	for i, audioFile := range audioFiles {
		if ctx.Err() != nil {
			break
		}

		duration, err := audio.GetDuration(audioFile)
		if err != nil {
			logger.Printf("Warning: could not get duration for %s: %v", audioFile, err)
			continue
		}

		chapterTitle := resolveChapterTitle(audioFile, i, m3uEntries)

		chapters = append(chapters, Chapter{
			Title:     chapterTitle,
			StartTime: currentTime,
		})

		currentTime += duration
		logger.Printf("Chapter %d: %s (%.2f seconds)", i+1, chapterTitle, duration)
	}

	if len(chapters) == 0 {
		logger.Printf("No chapters could be created from audio files.")
		return nil
	}

	logger.Printf("Created %d chapters from audio files.", len(chapters))
	return chapters
}

// Marker is an embedded boundary in the local source timeline.
type Marker struct {
	Title   string
	StartUS int64
}

// ReadEmbeddedMarkers extracts integer boundaries using libav.
// Returns nil, nil when the file contains no chapters.
func ReadEmbeddedMarkers(filePath string) ([]Marker, error) {
	fmtCtx := astiav.AllocFormatContext()
	if fmtCtx == nil {
		return nil, fmt.Errorf("allocating format context for %s", filePath)
	}
	defer fmtCtx.Free()

	if err := fmtCtx.OpenInput(filePath, nil, nil); err != nil {
		return nil, fmt.Errorf("opening input %s: %w", filePath, err)
	}
	defer fmtCtx.CloseInput()

	if err := fmtCtx.FindStreamInfo(nil); err != nil {
		return nil, fmt.Errorf("finding stream info for %s: %w", filePath, err)
	}

	markers, err := readChaptersCGO(fmtCtx)
	if err != nil {
		return nil, fmt.Errorf("reading chapters %s: %w", filePath, err)
	}
	return markers, nil
}

// ReadEmbeddedChapters retains the legacy seconds API as an integer adapter.
func ReadEmbeddedChapters(filePath string) ([]Chapter, error) {
	markers, err := ReadEmbeddedMarkers(filePath)
	if err != nil {
		return nil, err
	}
	if len(markers) == 0 {
		return nil, nil
	}
	chapters := make([]Chapter, len(markers))
	for i, m := range markers {
		chapters[i] = Chapter{Title: m.Title, StartTime: float64(m.StartUS) / 1000000}
	}
	return chapters, nil
}

// resolveChapterTitle determines a chapter title from M3U metadata or filename.
func resolveChapterTitle(audioFile string, index int, m3uEntries []audio.M3UEntry) string {
	baseFile := filepath.Base(audioFile)
	if index >= 0 && index < len(m3uEntries) && strings.TrimSpace(m3uEntries[index].Title) != "" {
		return strings.TrimSpace(m3uEntries[index].Title)
	}

	title := strings.TrimSuffix(baseFile, filepath.Ext(audioFile))

	prefixes := []string{
		fmt.Sprintf("%02d - ", index+1),
		fmt.Sprintf("%d - ", index+1),
		fmt.Sprintf("Chapter %d - ", index+1),
		fmt.Sprintf("Ch %d - ", index+1),
	}
	for _, prefix := range prefixes {
		title = strings.TrimPrefix(title, prefix)
	}
	title = strings.TrimSpace(title)

	if title == "" {
		title = fmt.Sprintf("Chapter %d", index+1)
	}
	return title
}
