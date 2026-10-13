package meta

import (
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// SidecarInfo lists companion files discovered in a book folder.
type SidecarInfo struct {
	NFO    []string
	CUE    []string
	M3U    []string
	Covers []string
}

var (
	nfoGlobPatterns   = []string{"*.nfo", "*.NFO", "*.info", "*.INFO"}
	cueGlobPatterns   = []string{"*.cue", "*.CUE"}
	m3uGlobPatterns   = []string{"*.m3u", "*.m3u8", "*.M3U"}
	coverGlobPatterns = []string{"*.jpg", "*.jpeg", "*.png", "*.webp", "*.gif", "*.JPG", "*.JPEG", "*.PNG"}
)

// DiscoverSidecars scans workDir for audiobook companion files.
func DiscoverSidecars(workDir string) SidecarInfo {
	return SidecarInfo{
		NFO:    globInDir(workDir, nfoGlobPatterns...),
		CUE:    globInDir(workDir, cueGlobPatterns...),
		M3U:    globInDir(workDir, m3uGlobPatterns...),
		Covers: globInDir(workDir, coverGlobPatterns...),
	}
}

// LogSidecars prints a summary of discovered companion files.
func LogSidecars(info SidecarInfo, logger *log.Logger) {
	logger.Printf("Scanning book folder for companion files...")
	logFiles := func(label string, files []string) {
		if len(files) == 0 {
			return
		}
		names := make([]string, len(files))
		for i, f := range files {
			names[i] = filepath.Base(f)
		}
		logger.Printf("  %s: %s", label, strings.Join(names, ", "))
	}
	logFiles("NFO/INFO", info.NFO)
	logFiles("CUE", info.CUE)
	logFiles("M3U", info.M3U)
	logFiles("Cover images", info.Covers)
	if len(info.NFO)+len(info.CUE)+len(info.M3U)+len(info.Covers) == 0 {
		logger.Printf("  (no companion files found)")
	}
}

// collectFromSources merges the first NFO/INFO sidecar in workDir (preferred)
// with tags embedded in audioFile, then applies directory-name defaults.
// found reports whether either source provided any metadata.
func collectFromSources(workDir, audioFile string, logger *log.Logger) (metadata AudiobookMetadata, found bool) {
	var nfoMeta, embedded AudiobookMetadata

	if nfoFiles := globInDir(workDir, nfoGlobPatterns...); len(nfoFiles) > 0 {
		m, err := parseNFOFile(nfoFiles[0], logger)
		if err != nil {
			logger.Printf("Warning: could not parse %s: %v", filepath.Base(nfoFiles[0]), err)
		} else {
			nfoMeta, found = m, true
		}
	}

	if audioFile != "" {
		m, err := ReadEmbeddedMetadata(audioFile)
		if err != nil {
			logger.Printf("Warning: could not read embedded metadata: %v", err)
		} else if m.Title != "" || m.Artist != "" {
			logger.Printf("Using metadata embedded in %s", filepath.Base(audioFile))
			embedded, found = m, true
		}
	}

	return mergeMetadata(nfoMeta, embedded, filepath.Base(workDir)), found
}

func mergeMetadata(primary, secondary AudiobookMetadata, defaultName string) AudiobookMetadata {
	title := coalesce(primary.Title, secondary.Title, defaultName)
	album := coalesce(primary.Album, secondary.Album, title)
	artist := coalesce(primary.Artist, secondary.Artist, "Unknown Artist")
	merged := AudiobookMetadata{
		Title:       title,
		Artist:      artist,
		Album:       album,
		AlbumArtist: coalesce(primary.AlbumArtist, secondary.AlbumArtist, artist),
		Composer:    coalesce(primary.Composer, secondary.Composer),
		Genre:       coalesce(primary.Genre, secondary.Genre, "Audiobook"),
		Date:        coalesce(primary.Date, secondary.Date),
		Copyright:   coalesce(primary.Copyright, secondary.Copyright),
		Comment:     coalesce(primary.Comment, secondary.Comment),
		Description: coalesce(primary.Description, secondary.Description),
		Subtitle:    coalesce(primary.Subtitle, secondary.Subtitle),
		Grouping:    coalesce(primary.Grouping, secondary.Grouping),
		Extra:       map[string]string{},
	}
	maps.Copy(merged.Extra, secondary.Extra)
	maps.Copy(merged.Extra, primary.Extra)
	return merged
}

func coalesce(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func globInDir(dir string, patterns ...string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, m := range matches {
			if fi, err := os.Stat(m); err != nil || fi.IsDir() {
				continue
			}
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
