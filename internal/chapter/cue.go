package chapter

import (
	"bufio"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CueTrack is a marker in the local timeline of its FILE source.
type CueTrack struct {
	FilePath string
	Number   int
	Title    string
	StartUS  int64
}

// ParseCueTracks reads sequential FILE/TRACK/INDEX directives without flattening timelines.
func ParseCueTracks(path string) ([]CueTrack, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cue %s: %w", path, err)
	}
	defer f.Close()
	var tracks []CueTrack
	var current *CueTrack
	file := ""
	hasIndex := false
	lineNo := 0
	trackLine := 0
	seen := map[string]bool{}
	last := int64(-1)
	fail := func(line int, msg string) ([]CueTrack, error) {
		return nil, fmt.Errorf("cue %s line %d FILE %q: %s", path, line, file, msg)
	}
	finish := func() error {
		if current != nil {
			if !hasIndex {
				return fmt.Errorf("track %d missing INDEX 01 (line %d)", current.Number, trackLine)
			}
			tracks = append(tracks, *current)
			current = nil
		}
		return nil
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		token := strings.ToUpper(parts[0])
		rest := strings.TrimSpace(line[len(parts[0]):])
		switch token {
		case "FILE":
			if err := finish(); err != nil {
				return fail(lineNo, err.Error())
			}
			name, tail, err := cueValue(rest)
			if err != nil || strings.TrimSpace(tail) == "" {
				return fail(lineNo, "invalid FILE")
			}
			p := name
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(path), p)
			}
			p, err = filepath.Abs(p)
			if err != nil {
				return fail(lineNo, err.Error())
			}
			// Resolve an existing symlink, while allowing parsing independently of probing.
			if resolved, e := filepath.EvalSymlinks(p); e == nil {
				p = resolved
			}
			if seen[p] && p != file {
				return fail(lineNo, "noncontiguous repeated FILE "+p)
			}
			if p != file {
				last = -1
			}
			seen[p] = true
			file = p
		case "TRACK":
			if err := finish(); err != nil {
				return fail(lineNo, err.Error())
			}
			if file == "" {
				return fail(lineNo, "TRACK requires FILE")
			}
			args := strings.Fields(rest)
			if len(args) != 2 || !strings.EqualFold(args[1], "AUDIO") {
				return fail(lineNo, "only AUDIO tracks are supported")
			}
			n, e := strconv.Atoi(args[0])
			if e != nil || n <= 0 {
				return fail(lineNo, "invalid TRACK number")
			}
			current = &CueTrack{FilePath: file, Number: n, Title: fmt.Sprintf("Chapter %d", n)}
			hasIndex = false
			trackLine = lineNo
		case "TITLE":
			title, tail, e := cueValue(rest)
			if e != nil || strings.TrimSpace(tail) != "" {
				return fail(lineNo, "invalid TITLE")
			}
			if current != nil && strings.TrimSpace(title) != "" {
				current.Title = title
			}
		case "INDEX":
			if file == "" || current == nil {
				return fail(lineNo, "INDEX requires FILE and TRACK")
			}
			args := strings.Fields(rest)
			if len(args) != 2 || (args[0] != "00" && args[0] != "01") {
				return fail(lineNo, "unsupported INDEX")
			}
			us, e := cueTimeUS(args[1])
			if e != nil {
				return fail(lineNo, e.Error())
			}
			if args[0] == "00" {
				continue
			}
			if hasIndex {
				return fail(lineNo, "duplicate INDEX 01")
			}
			if us <= last {
				return fail(lineNo, "decreasing or duplicate INDEX 01")
			}
			current.StartUS = us
			hasIndex = true
			last = us
		case "PREGAP", "POSTGAP":
			return fail(lineNo, "unsupported synthetic "+token)
		}
	}
	if err := scanner.Err(); err != nil {
		return fail(lineNo, err.Error())
	}
	if err := finish(); err != nil {
		return fail(lineNo, err.Error())
	}
	if len(tracks) == 0 {
		return fail(lineNo, "no AUDIO tracks")
	}
	return tracks, nil
}

func cueValue(s string) (string, string, error) {
	if strings.HasPrefix(s, "\"") {
		end := strings.Index(s[1:], "\"")
		if end < 0 {
			return "", "", fmt.Errorf("unclosed quote")
		}
		return s[1 : end+1], s[end+2:], nil
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return "", "", fmt.Errorf("missing value")
	}
	return parts[0], strings.TrimSpace(s[len(parts[0]):]), nil
}
func cueTimeUS(s string) (int64, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid INDEX time %q", s)
	}
	var v [3]int64
	for i, p := range parts {
		n, e := strconv.ParseInt(p, 10, 64)
		if e != nil || n < 0 {
			return 0, fmt.Errorf("invalid INDEX time %q", s)
		}
		v[i] = n
	}
	if v[1] > 59 || v[2] > 74 || v[0] > (math.MaxInt64-59)/60 {
		return 0, fmt.Errorf("invalid or overflowing INDEX time %q", s)
	}
	seconds := v[0]*60 + v[1]
	// Split whole seconds from the fractional frame before scaling, so a
	// representable microsecond result never overflows a frame intermediate.
	fraction := (v[2]*1000000 + 37) / 75
	if seconds > (math.MaxInt64-fraction)/1000000 {
		return 0, fmt.Errorf("overflowing INDEX time %q", s)
	}
	return seconds*1000000 + fraction, nil
}

// ParseCueFile retains the legacy single-source seconds API.
func ParseCueFile(path string, logger *log.Logger) ([]Chapter, error) {
	tracks, err := ParseCueTracks(path)
	if err != nil {
		return nil, err
	}
	var chapters []Chapter
	for _, track := range tracks {
		if track.FilePath != tracks[0].FilePath {
			return nil, fmt.Errorf("cue %s: multiple FILE sources require ParseCueTracks", path)
		}
		chapters = append(chapters, Chapter{Title: track.Title, StartTime: float64(track.StartUS) / 1000000})
	}
	if logger != nil {
		logger.Printf("Extracted %d chapters from CUE file", len(chapters))
	}
	return chapters, nil
}
