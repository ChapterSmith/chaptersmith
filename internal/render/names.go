package render

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// chapterFilename takes a one-based ordinal and preserves the title's Unicode spelling.
func chapterFilename(index, total int, title, format string) (string, error) {
	if index < 1 || total < 1 || index > total {
		return "", fmt.Errorf("invalid chapter ordinal %d/%d", index, total)
	}
	switch format {
	case "m4b":
		format = "m4a"
	case "m4a", "flac", "wav":
	default:
		return "", fmt.Errorf("unsupported chapter format %q", format)
	}
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, title)
	title = strings.TrimRight(title, ". ")
	if title == "" {
		title = "Chapter"
	}
	base, _, _ := strings.Cut(title, ".")
	upper := strings.ToUpper(base)
	reserved := upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" || upper == "CONIN$" || upper == "CONOUT$"
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		reserved = true
	}
	if reserved {
		title = "_" + title
	}
	if len(title) > 120 {
		n := 120
		for !utf8.RuneStart(title[n]) {
			n--
		}
		title = title[:n]
	}
	title = strings.TrimRight(title, ". ")
	if title == "" {
		title = "Chapter"
	}
	return fmt.Sprintf("%0*d — %s.%s", max(3, len(strconv.Itoa(total))), index, title, format), nil
}
