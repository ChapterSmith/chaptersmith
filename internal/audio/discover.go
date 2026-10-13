package audio

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// M3UEntry represents a single entry parsed from an M3U playlist.
type M3UEntry struct {
	Filename string
	Title    string
	Duration int
}

// AudioExtensions defines supported audio file types.
var AudioExtensions = map[string]bool{
	".m4b":  true,
	".m4a":  true,
	".mp3":  true,
	".aac":  true,
	".ogg":  true,
	".flac": true,
	".wav":  true,
	".opus": true,
}

// numberRe is compiled once for natural sorting of filenames.
var numberRe = regexp.MustCompile(`\d+`)

// FindAudioFiles discovers audio files from an M3U playlist or directory listing.
// Returns the file list and any M3U entries (nil when no M3U was used).
// excludeFile is the output path to skip; relative paths are resolved against dir.
func FindAudioFiles(dir, excludeFile string) ([]string, []M3UEntry, error) {
	return FindAudioFilesWithPlaylist(dir, excludeFile, "")
}

// FindAudioFilesWithPlaylist selects an explicit playlist or requires unambiguous discovery.
func FindAudioFilesWithPlaylist(dir, excludeFile, playlistPath string) ([]string, []M3UEntry, error) {
	if excludeFile != "" && !filepath.IsAbs(excludeFile) {
		excludeFile = filepath.Join(dir, excludeFile)
	}
	if playlistPath != "" {
		p, err := filepath.Abs(playlistPath)
		if err != nil {
			return nil, nil, fmt.Errorf("playlist %s: %w", playlistPath, err)
		}
		return parsePlaylistExcludingOutput(p, excludeFile)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading directory %s: %w", dir, err)
	}
	var candidates []string
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if !e.IsDir() && (strings.EqualFold(ext, ".m3u") || strings.EqualFold(ext, ".m3u8")) {
			p, err := filepath.Abs(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, nil, err
			}
			candidates = append(candidates, p)
		}
	}
	sort.Strings(candidates)
	if len(candidates) > 1 {
		return nil, nil, fmt.Errorf("multiple playlists: %s; set PlaylistPath", strings.Join(candidates, ", "))
	}
	if len(candidates) == 1 {
		return parsePlaylistExcludingOutput(candidates[0], excludeFile)
	}
	files, err := ListAudioFiles(dir, excludeFile)
	return files, nil, err
}

// ListAudioFiles returns supported regular files in natural order, without selecting playlists.
func ListAudioFiles(dir, excludeFile string) ([]string, error) {
	if excludeFile != "" && !filepath.IsAbs(excludeFile) {
		excludeFile = filepath.Join(dir, excludeFile)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if sameFile(filepath.Join(dir, e.Name()), excludeFile) || !AudioExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		p, err := filepath.Abs(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if !st.Mode().IsRegular() {
			continue
		}
		files = append(files, p)
	}
	SortFilesNaturally(files)
	return files, nil
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil && absA == absB {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func parsePlaylistExcludingOutput(path, output string) ([]string, []M3UEntry, error) {
	files, entries, err := ParseM3UFile(path, filepath.Dir(path))
	if err != nil {
		return nil, nil, err
	}
	filteredFiles := make([]string, 0, len(files))
	filteredEntries := make([]M3UEntry, 0, len(entries))
	for i, file := range files {
		if !sameFile(file, output) {
			filteredFiles = append(filteredFiles, file)
			filteredEntries = append(filteredEntries, entries[i])
		}
	}
	return filteredFiles, filteredEntries, nil
}

// ParseM3UFile preserves every ordered occurrence and rejects invalid entries as a whole.
func ParseM3UFile(m3uFile, baseDir string) ([]string, []M3UEntry, error) {
	f, err := os.Open(m3uFile)
	if err != nil {
		return nil, nil, fmt.Errorf("playlist %s: %w", m3uFile, err)
	}
	defer f.Close()
	var files []string
	var entries []M3UEntry
	var title string
	var duration int
	scanner := bufio.NewScanner(f)
	lineNo := 0
	fail := func(err error) ([]string, []M3UEntry, error) {
		return nil, nil, fmt.Errorf("playlist %s line %d: %w", m3uFile, lineNo, err)
	}
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(line), "#EXTINF:") {
			title = ""
			duration = 0
			parts := strings.SplitN(line[len("#EXTINF:"):], ",", 2)
			if len(parts) == 2 {
				if parsed, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
					duration = parsed
				}
				title = strings.TrimSpace(parts[1])
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		u, err := url.Parse(line)
		if err == nil && u.Scheme != "" && !filepath.IsAbs(line) {
			return fail(fmt.Errorf("network or nonlocal URL %q is unsupported", line))
		}
		p := line
		if !filepath.IsAbs(p) {
			p = filepath.Join(baseDir, p)
		}
		p, err = filepath.Abs(p)
		if err != nil {
			return fail(err)
		}
		if !AudioExtensions[strings.ToLower(filepath.Ext(p))] {
			return fail(fmt.Errorf("unsupported audio entry %q", line))
		}
		st, err := os.Stat(p)
		if err != nil {
			return fail(fmt.Errorf("entry %q: %w", line, err))
		}
		if !st.Mode().IsRegular() {
			return fail(fmt.Errorf("entry %q is not a regular file", line))
		}
		files = append(files, p)
		entries = append(entries, M3UEntry{Filename: p, Title: title, Duration: duration})
		title = ""
		duration = 0
	}
	if err := scanner.Err(); err != nil {
		return fail(err)
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("playlist %s: empty playlist", m3uFile)
	}
	return files, entries, nil
}

// SortFilesNaturally sorts filenames so that embedded numbers are compared
// numerically rather than lexicographically.
func SortFilesNaturally(files []string) {
	sort.Slice(files, func(i, j int) bool {
		ni := extractNumbers(files[i])
		nj := extractNumbers(files[j])

		for k := 0; k < len(ni) && k < len(nj); k++ {
			if ni[k] != nj[k] {
				return ni[k] < nj[k]
			}
		}

		if len(ni) != len(nj) {
			return len(ni) < len(nj)
		}

		return files[i] < files[j]
	})
}

func extractNumbers(s string) []int {
	matches := numberRe.FindAllString(s, -1)
	result := make([]int, 0, len(matches))
	for _, m := range matches {
		n, _ := strconv.Atoi(m)
		result = append(result, n)
	}
	return result
}
