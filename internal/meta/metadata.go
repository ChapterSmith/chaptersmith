package meta

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/asticode/go-astiav"
)

// AudiobookMetadata holds metadata tags for the final audiobook file.
type AudiobookMetadata struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist"`
	Composer    string `json:"composer"`
	Genre       string `json:"genre"`
	Date        string `json:"date"` // year or full date (e.g. "2026" or "2026-09-15")
	Copyright   string `json:"copyright"`
	Comment     string `json:"comment"`
	Description string `json:"description"`
	Subtitle    string `json:"subtitle"`
	Grouping    string `json:"grouping"`
	// Extra holds additional source tags to preserve (series, language, ASIN, etc.).
	Extra map[string]string `json:"extra"`
}

// skipEmbeddedKeys are container/encoder tags that should not be copied to output.
var skipEmbeddedKeys = map[string]bool{
	"major_brand":       true,
	"minor_version":     true,
	"compatible_brands": true,
	"encoder":           true,
	"encoded_by":        true,
	"creation_time":     true,
	"handler_name":      true,
	"vendor_id":         true,
	"audible_drm_type":  true,
	"audible_acr":       true,
	// Per-file position tags are meaningless for a merged or re-encoded book.
	"track": true,
	"disc":  true,
}

// knownMetadataKeys are written via named fields; excluded from Extra to avoid duplicates.
var knownMetadataKeys = map[string]bool{
	"title": true, "artist": true, "album": true, "album_artist": true,
	"composer": true, "genre": true, "date": true, "year": true,
	"copyright": true, "comment": true, "description": true,
	"synopsis": true, "subtitle": true, "grouping": true,
}

// NFO field patterns match common audiobook NFO formats including
// Author, Read By, Narrator, etc.
var (
	nfoTitleRe     = regexp.MustCompile(`(?im)^\s*title\s*[=:]\s*(.+)`)
	nfoArtistRe    = regexp.MustCompile(`(?im)^\s*(?:artist|author|writer|by)\s*[=:]\s*(.+)`)
	nfoAlbumRe     = regexp.MustCompile(`(?im)^\s*(?:album|series)\s*[=:]\s*(.+)`)
	nfoComposerRe  = regexp.MustCompile(`(?im)^\s*(?:composer|narrator|read\s*by)\s*[=:]\s*(.+)`)
	nfoGenreRe     = regexp.MustCompile(`(?im)^\s*(?:genre|category)\s*[=:]\s*(.+)`)
	nfoCopyrightRe = regexp.MustCompile(`(?im)^\s*(?:copyright|©)\s*[=:]\s*(.+)`)
	nfoYearRe      = regexp.MustCompile(`(?im)^\s*(?:year|date|published|copyright\s*year)\s*[=:]\s*(.+)`)
	nfoCommentRe   = regexp.MustCompile(`(?im)^\s*(?:comment|description|summary)\s*[=:]\s*(.+)`)
)

// CollectMetadataFromDir combines NFO/INFO sidecars in workDir with tags
// embedded in firstAudioFile, falling back to directory-name defaults
// without prompting. Use this when stdin is not available (e.g. GUI).
func CollectMetadataFromDir(workDir, firstAudioFile string, logger *log.Logger) AudiobookMetadata {
	metadata, _ := collectFromSources(workDir, firstAudioFile, logger)
	logMetadata(metadata, logger)
	return metadata
}

// CollectMetadata combines NFO/INFO sidecars in workDir with tags embedded
// in firstAudioFile; when neither provides anything, it collects metadata
// interactively from stdin.
func CollectMetadata(workDir, firstAudioFile string, logger *log.Logger) AudiobookMetadata {
	metadata, found := collectFromSources(workDir, firstAudioFile, logger)
	if !found {
		metadata = collectMetadataInteractive(filepath.Base(workDir), logger)
	}
	logMetadata(metadata, logger)
	return metadata
}

// parseNFOFile returns only the fields present in the NFO; defaults are
// applied later so they never override embedded tags.
func parseNFOFile(nfoFile string, logger *log.Logger) (AudiobookMetadata, error) {
	logger.Printf("Parsing metadata from: %s", filepath.Base(nfoFile))

	content, err := os.ReadFile(nfoFile)
	if err != nil {
		return AudiobookMetadata{}, fmt.Errorf("reading nfo file: %w", err)
	}

	contentStr := string(content)
	var metadata AudiobookMetadata

	if m := nfoTitleRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Title = strings.TrimSpace(m[1])
	}
	if m := nfoArtistRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Artist = strings.TrimSpace(m[1])
	}
	if m := nfoAlbumRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Album = strings.TrimSpace(m[1])
	}
	if m := nfoComposerRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Composer = strings.TrimSpace(m[1])
	}
	if m := nfoGenreRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Genre = strings.TrimSpace(m[1])
	}
	if m := nfoCopyrightRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Copyright = strings.TrimSpace(m[1])
	}
	if m := nfoYearRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Date = strings.TrimSpace(m[1])
	}
	if m := nfoCommentRe.FindStringSubmatch(contentStr); len(m) > 1 {
		metadata.Comment = strings.TrimSpace(m[1])
	}

	return metadata, nil
}

// ReadEmbeddedMetadata extracts metadata tags from an audio file's container
// using the libav API, including genre, copyright, date, comment/description,
// and other audiobook-relevant tags.
// Returns a zero-value AudiobookMetadata (not an error) when tags are absent.
func ReadEmbeddedMetadata(filePath string) (AudiobookMetadata, error) {
	fmtCtx := astiav.AllocFormatContext()
	if fmtCtx == nil {
		return AudiobookMetadata{}, fmt.Errorf("allocating format context for %s", filePath)
	}
	defer fmtCtx.Free()

	if err := fmtCtx.OpenInput(filePath, nil, nil); err != nil {
		return AudiobookMetadata{}, fmt.Errorf("opening input %s: %w", filePath, err)
	}
	defer fmtCtx.CloseInput()

	if err := fmtCtx.FindStreamInfo(nil); err != nil {
		return AudiobookMetadata{}, fmt.Errorf("finding stream info for %s: %w", filePath, err)
	}

	md := fmtCtx.Metadata()
	if md == nil {
		return AudiobookMetadata{}, nil
	}

	get := func(keys ...string) string {
		for _, key := range keys {
			entry := md.Get(key, nil, 0)
			if entry == nil {
				continue
			}
			if v := strings.TrimSpace(entry.Value()); v != "" {
				return v
			}
		}
		return ""
	}

	out := AudiobookMetadata{
		Title:       get("title"),
		Artist:      get("artist"),
		Album:       get("album"),
		AlbumArtist: get("album_artist"),
		Composer:    get("composer"),
		Genre:       get("genre"),
		Date:        get("date", "year"),
		Copyright:   get("copyright"),
		Comment:     get("comment"),
		Description: get("synopsis", "description"), // desc may be truncated to 255 chars
		Subtitle:    get("subtitle"),
		Grouping:    get("grouping"),
		Extra:       map[string]string{},
	}

	var prev *astiav.DictionaryEntry
	for {
		entry := md.Get("", prev, astiav.NewDictionaryFlags(astiav.DictionaryFlagIgnoreSuffix))
		if entry == nil {
			break
		}
		prev = entry
		key := strings.ToLower(strings.TrimSpace(entry.Key()))
		val := strings.TrimSpace(entry.Value())
		if key == "" || val == "" || skipEmbeddedKeys[key] || knownMetadataKeys[key] {
			continue
		}
		out.Extra[key] = val
	}

	return out, nil
}

func logMetadata(metadata AudiobookMetadata, logger *log.Logger) {
	logger.Printf("\nUsing metadata:")
	logger.Printf("  Title: %s", metadata.Title)
	logger.Printf("  Artist: %s", metadata.Artist)
	logger.Printf("  Album: %s", metadata.Album)
	if metadata.Composer != "" {
		logger.Printf("  Composer/Narrator: %s", metadata.Composer)
	}
	if metadata.Genre != "" {
		logger.Printf("  Genre: %s", metadata.Genre)
	}
	if metadata.Date != "" {
		logger.Printf("  Date: %s", metadata.Date)
	}
	if metadata.Copyright != "" {
		logger.Printf("  Copyright: %s", metadata.Copyright)
	}
	if metadata.Subtitle != "" {
		logger.Printf("  Subtitle: %s", metadata.Subtitle)
	}
	if metadata.Comment != "" || metadata.Description != "" {
		logger.Printf("  Description/Comment: preserved")
	}
	if len(metadata.Extra) > 0 {
		logger.Printf("  Extra tags: %d read (only iTunes-supported keys are written)", len(metadata.Extra))
	}
}
