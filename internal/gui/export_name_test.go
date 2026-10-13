package gui

import (
	"strings"
	"testing"
)

func TestSuggestedExportNameUsesTitleAndAuthorWhenPresent(t *testing.T) {
	for _, tc := range []struct{ title, author, want string }{
		{"Monster Makers", "E.A. Winters", "Monster Makers-E.A. Winters"},
		{"Monster Makers", "", "Monster Makers"},
		{"", "E.A. Winters", "E.A. Winters"},
		{"", "", "book"},
		{"  Part 1: The/End?  ", "A\tB", "Part 1 The End-A B"},
		{"...", "", "book"},
		{"CON", "", "book"},
		{"Ends with dot.", "", "Ends with dot"},
	} {
		if got := suggestedExportName(tc.title, tc.author); got != tc.want {
			t.Errorf("title %q author %q: %q, want %q", tc.title, tc.author, got, tc.want)
		}
	}
	long := suggestedExportName(strings.Repeat("ż", 400), "Author")
	if n := len([]rune(long)); n > 150 || !validExportComponent(long) {
		t.Errorf("long name has %d characters or is not a safe file name", n)
	}
}
