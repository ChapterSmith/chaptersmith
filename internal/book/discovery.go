package book

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/chapter"
)

// ImportInputs describes ordered source occurrences, without probing or cache writes.
type ImportInputs struct {
	WorkDir      string
	Files        []string
	Entries      []audio.M3UEntry
	CuePath      string
	PlaylistPath string
}

func importChapterMode(opts ImportOptions) (string, error) {
	mode := strings.ToLower(opts.ChapterSource)
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "embedded", "files", "cue":
	default:
		return "", fmt.Errorf("invalid ChapterSource %q; select auto, embedded, files or cue", opts.ChapterSource)
	}
	if opts.CuePath != "" {
		if mode == "embedded" || mode == "files" {
			return "", fmt.Errorf("CuePath %s conflicts with ChapterSource=%s", opts.CuePath, mode)
		}
		mode = "cue"
	}
	return mode, nil
}

// DiscoverInputs resolves selections and canonical source paths without requiring CacheDir.
// excludeFile excludes a CLI output only from folder discovery; explicit sources stay selected.
func DiscoverInputs(opts ImportOptions, excludeFile string) (ImportInputs, error) {
	mode, err := importChapterMode(opts)
	if err != nil {
		return ImportInputs{}, err
	}
	if opts.InputPath == "" {
		return ImportInputs{}, fmt.Errorf("InputPath is required")
	}
	input, err := canonicalPath(opts.InputPath)
	if err != nil {
		return ImportInputs{}, fmt.Errorf("InputPath %s: %w", opts.InputPath, err)
	}
	st, err := os.Stat(input)
	if err != nil {
		return ImportInputs{}, err
	}
	result := ImportInputs{WorkDir: input}
	single := !st.IsDir()
	if single {
		if !st.Mode().IsRegular() || !audio.AudioExtensions[strings.ToLower(filepath.Ext(opts.InputPath))] {
			return ImportInputs{}, fmt.Errorf("InputPath %s must be a directory or supported regular audio file", opts.InputPath)
		}
		if opts.PlaylistPath != "" {
			return ImportInputs{}, fmt.Errorf("PlaylistPath is not allowed for single-file InputPath %s", input)
		}
		result.WorkDir = filepath.Dir(input)
		result.Files = []string{input}
	}
	if opts.CuePath != "" {
		result.CuePath, err = canonicalPath(opts.CuePath)
		if err != nil {
			return ImportInputs{}, fmt.Errorf("CuePath %s: %w", opts.CuePath, err)
		}
	} else if !single && (mode == "auto" || mode == "cue") {
		var candidates []string
		entries, err := os.ReadDir(input)
		if err != nil {
			return ImportInputs{}, err
		}
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".cue") {
				candidates = append(candidates, filepath.Join(input, e.Name()))
			}
		}
		sort.Strings(candidates)
		if len(candidates) > 1 {
			return ImportInputs{}, fmt.Errorf("multiple CUE candidates: %s; set CuePath or select ChapterSource=embedded or files", strings.Join(candidates, ", "))
		}
		if len(candidates) == 1 {
			result.CuePath, err = canonicalPath(candidates[0])
			if err != nil {
				return ImportInputs{}, err
			}
		}
	}
	if mode == "cue" && result.CuePath == "" {
		return ImportInputs{}, fmt.Errorf("ChapterSource=cue requires CuePath or exactly one CUE candidate")
	}
	if !single {
		playlistPath, err := selectImportPlaylist(input, opts.PlaylistPath)
		if err != nil {
			return ImportInputs{}, err
		}
		switch {
		case playlistPath != "":
			result.Files, result.Entries, err = audio.FindAudioFilesWithPlaylist(input, "", playlistPath)
			if err == nil {
				result.PlaylistPath, err = canonicalPath(playlistPath)
			}
		case result.CuePath == "":
			result.Files, err = audio.ListAudioFiles(input, excludeFile)
		}
		if err != nil {
			return ImportInputs{}, err
		}
	}
	if result.CuePath != "" {
		tracks, err := chapter.ParseCueTracks(result.CuePath)
		if err != nil {
			return ImportInputs{}, err
		}
		var cueFiles []string
		for _, track := range tracks {
			p, err := canonicalAudioFile(track.FilePath)
			if err != nil {
				return ImportInputs{}, fmt.Errorf("CUE %s FILE %s: %w", result.CuePath, track.FilePath, err)
			}
			if len(cueFiles) == 0 || cueFiles[len(cueFiles)-1] != p {
				cueFiles = append(cueFiles, p)
			}
		}
		if single {
			if len(cueFiles) != 1 || cueFiles[0] != input {
				return ImportInputs{}, fmt.Errorf("CuePath %s must refer exclusively to single-file InputPath %s", result.CuePath, input)
			}
		} else if len(result.Entries) == 0 {
			result.Files = cueFiles
		}
	}
	if len(result.Files) == 0 {
		return ImportInputs{}, fmt.Errorf("no supported audio files in InputPath %s", input)
	}
	for i, path := range result.Files {
		p, err := canonicalAudioFile(path)
		if err != nil {
			return ImportInputs{}, err
		}
		result.Files[i] = p
		if len(result.Entries) > 0 {
			result.Entries[i].Filename = p
		}
	}
	return result, nil
}

// selectImportPlaylist selects a playlist without statting unrelated audio files.
// Returning no selection leaves CUE FILE order or plain-folder listing to the caller.
func selectImportPlaylist(dir, explicitPath string) (string, error) {
	if explicitPath != "" {
		return explicitPath, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading directory %s: %w", dir, err)
	}
	var candidates []string
	for _, entry := range entries {
		extension := filepath.Ext(entry.Name())
		if !entry.IsDir() && (strings.EqualFold(extension, ".m3u") || strings.EqualFold(extension, ".m3u8")) {
			candidates = append(candidates, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(candidates)
	if len(candidates) > 1 {
		return "", fmt.Errorf("multiple playlists: %s; set PlaylistPath", strings.Join(candidates, ", "))
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", nil
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}
func canonicalAudioFile(path string) (string, error) {
	if !audio.AudioExtensions[strings.ToLower(filepath.Ext(path))] {
		return "", fmt.Errorf("unsupported audio file %s", path)
	}
	p, err := canonicalPath(path)
	if err != nil {
		return "", fmt.Errorf("source %s: %w", path, err)
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("source %s is not a regular file", path)
	}
	return p, nil
}
