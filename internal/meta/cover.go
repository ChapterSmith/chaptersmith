package meta

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/asticode/go-astiav"
)

// HandleCoverImage finds an existing cover image in workDir, or
// extracts an embedded cover from the first audio file using libav.
// Returns the cover file path (absolute) and whether a cover is available.
func HandleCoverImage(workDir, firstAudioFile, tempDir string, logger *log.Logger) (string, bool) {
	if cover, ok := FindCoverImage(workDir, firstAudioFile, logger); ok {
		return cover, true
	}

	logger.Print("Cover not found, attempting extraction from first audio file...")

	coverPath, err := extractCoverFromAudio(firstAudioFile, tempDir)
	if err != nil {
		logger.Printf("Failed to extract cover: %v; proceeding without cover.", err)
		return "", false
	}

	logger.Print("Successfully extracted cover from audio file.")
	return coverPath, true
}

// FindCoverImage searches workDir for a cover image, preferring names that
// match common audiobook conventions and the audio file basename.
func FindCoverImage(workDir, audioFile string, logger *log.Logger) (string, bool) {
	base := strings.TrimSuffix(filepath.Base(audioFile), filepath.Ext(audioFile))
	priorityNames := []string{"cover", "folder", "front", "album", "artwork", "icon", base}
	exts := []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".JPG", ".JPEG", ".PNG"}

	for _, name := range priorityNames {
		for _, ext := range exts {
			p := filepath.Join(workDir, name+ext)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				logger.Printf("Using cover image: %s", filepath.Base(p))
				return p, true
			}
		}
	}

	for _, pattern := range coverGlobPatterns {
		matches, _ := filepath.Glob(filepath.Join(workDir, pattern))
		if len(matches) > 0 {
			logger.Printf("Using cover image: %s", filepath.Base(matches[0]))
			return matches[0], true
		}
	}

	return "", false
}

// extractCoverFromAudio retains the legacy wrapper until the CLI uses managed covers.
func extractCoverFromAudio(audioFile, workDir string) (string, error) {
	data, extension, err := readAttachedCover(audioFile)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("no attached cover art found")
	}
	coverPath := filepath.Join(workDir, "cover"+extension)
	if err := os.WriteFile(coverPath, data, 0644); err != nil {
		return "", fmt.Errorf("writing cover file: %w", err)
	}
	return coverPath, nil
}

// readAttachedCover reads artwork bytes without writing anything beside the source.
// Ordinary video streams are not cover art.
func readAttachedCover(audioFile string) ([]byte, string, error) {
	fc := astiav.AllocFormatContext()
	if fc == nil {
		return nil, "", errors.New("allocating cover format context")
	}
	defer fc.Free()
	if err := fc.OpenInput(audioFile, nil, nil); err != nil {
		return nil, "", fmt.Errorf("opening cover source %s: %w", audioFile, err)
	}
	defer fc.CloseInput()
	if err := fc.FindStreamInfo(nil); err != nil {
		return nil, "", fmt.Errorf("finding cover stream info for %s: %w", audioFile, err)
	}
	index := -1
	for _, s := range fc.Streams() {
		if s.CodecParameters().MediaType() == astiav.MediaTypeVideo && s.DispositionFlags().Has(astiav.DispositionFlagAttachedPic) {
			index = s.Index()
			break
		}
	}
	if index < 0 {
		return nil, "", nil
	}
	pkt := astiav.AllocPacket()
	if pkt == nil {
		return nil, "", errors.New("allocating cover packet")
	}
	defer pkt.Free()
	for {
		if err := fc.ReadFrame(pkt); err != nil {
			if errors.Is(err, astiav.ErrEof) {
				return nil, "", fmt.Errorf("attached cover in %s has no image packet", audioFile)
			}
			return nil, "", fmt.Errorf("reading cover frame: %w", err)
		}
		if pkt.StreamIndex() == index {
			data := append([]byte(nil), pkt.Data()...)
			pkt.Unref()
			if len(data) == 0 {
				return nil, "", fmt.Errorf("attached cover in %s is empty", audioFile)
			}
			validated, extension, err := validateCoverBytes(data, false)
			if err != nil {
				return nil, "", fmt.Errorf("attached cover in %s: %w", audioFile, err)
			}
			return validated, extension, nil
		}
		pkt.Unref()
	}
}
