package book

import (
	"errors"
	"fmt"
	"os"
)

// CheckSources reports every missing, nonregular or fingerprint-changed source.
// It never modifies the saved source fingerprints.
func CheckSources(project BookProject) error {
	var failures []error
	for _, source := range project.Sources {
		st, err := os.Stat(source.Path)
		if err != nil {
			failures = append(failures, fmt.Errorf("source %s: %w", source.Path, err))
			continue
		}
		if !st.Mode().IsRegular() {
			failures = append(failures, fmt.Errorf("source %s is no longer a regular file", source.Path))
			continue
		}
		if st.Size() != source.SizeBytes || st.ModTime().UnixNano() != source.MTimeNS {
			failures = append(failures, fmt.Errorf("source %s changed: size %d (saved %d), mtime %d (saved %d)", source.Path, st.Size(), source.SizeBytes, st.ModTime().UnixNano(), source.MTimeNS))
		}
	}
	return errors.Join(failures...)
}
