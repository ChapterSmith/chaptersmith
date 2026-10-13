package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type sourceReader interface {
	io.Reader
	io.Closer
	Stat() (os.FileInfo, error)
}
type openSource func(string) (sourceReader, error)

func sourceHash(ctx context.Context, s Source, open openSource) (fp Fingerprint, err error) {
	if err = ctx.Err(); err != nil {
		return fp, err
	}
	file, err := open(s.Path)
	if err != nil {
		return fp, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	before, err := file.Stat()
	if err != nil {
		return fp, err
	}
	if !before.Mode().IsRegular() || before.Size() != s.SizeBytes || before.ModTime().UnixNano() != s.MTimeNS {
		return fp, fmt.Errorf("source %q changed or is not regular", s.ID)
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err = ctx.Err(); err != nil {
			return fp, err
		}
		var n int
		n, err = file.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fp, err
		}
		if n == 0 {
			return fp, io.ErrNoProgress
		}
	}
	after, err := file.Stat()
	if err != nil {
		return fp, err
	}
	pathInfo, err := os.Stat(s.Path)
	if err != nil {
		return fp, err
	}
	if !os.SameFile(before, after) || !os.SameFile(before, pathInfo) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Size() != pathInfo.Size() || before.ModTime() != pathInfo.ModTime() {
		return fp, fmt.Errorf("source %q changed while hashing", s.ID)
	}
	return Fingerprint{SourceID: s.ID, Name: filepath.Base(s.Path), SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: before.Size(), MTimeNS: before.ModTime().UnixNano(), identity: before}, nil
}
func equalFingerprint(a, b Fingerprint) bool {
	return a.SourceID == b.SourceID && a.SizeBytes == b.SizeBytes && a.MTimeNS == b.MTimeNS && a.SHA256 == b.SHA256 && (a.identity == nil || b.identity != nil && os.SameFile(a.identity, b.identity))
}

// CheckSources rehashes the original files, including same-size/same-mtime edits.
// Keep expected fingerprints in the service's private snapshot, preserving identity.
func CheckSources(ctx context.Context, input Input, expected []Fingerprint) error {
	in, _, err := validateInput(input)
	if err != nil {
		return err
	}
	return checkSources(ctx, in, expected, defaultDependencies().open, nil)
}
func checkSources(ctx context.Context, in Input, expected []Fingerprint, open openSource, progress func(Progress)) error {
	if len(in.Sources) != len(expected) {
		return errors.New("source fingerprint set changed")
	}
	byID := make(map[string]Fingerprint, len(expected))
	for _, fp := range expected {
		if _, ok := byID[fp.SourceID]; ok {
			return errors.New("duplicate source fingerprint")
		}
		byID[fp.SourceID] = fp
	}
	for i, s := range in.Sources {
		if progress != nil {
			progress(Progress{Phase: "hash-after", Current: int64(i), Total: int64(len(in.Sources))})
		}
		fp, err := sourceHash(ctx, s, open)
		if err != nil {
			return err
		}
		old, ok := byID[s.ID]
		if !ok || !equalFingerprint(old, fp) {
			return fmt.Errorf("source %q content or identity changed", s.ID)
		}
	}
	if progress != nil {
		progress(Progress{Phase: "hash-after", Current: int64(len(in.Sources)), Total: int64(len(in.Sources))})
	}
	return ctx.Err()
}
