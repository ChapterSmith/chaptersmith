// Package analysiscache owns ephemeral analysis scratch for one application process.
// It never treats a PID, timestamp, or directory name alone as proof of ownership.
package analysiscache

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const application = "audiobook-merger-analysis"
const markerLimit = 4096

var errIdentity = errors.New("analysis scratch ownership changed")

type marker struct {
	Application string `json:"application"`
	Version     int    `json:"version"`
	OwnerID     string `json:"ownerId"`
}

// Owner holds an exclusive OS lock for the lifetime of its private directory.
// Callers must cancel and join all operations and close their scratch files before
// Close. The lock is released by the OS if the application exits unexpectedly.
type Owner struct {
	dir        string
	name       string
	parent     *os.Root
	root       *os.Root
	directory  *os.File
	dirInfo    os.FileInfo
	lock       *os.File
	lockInfo   os.FileInfo
	markerInfo os.FileInfo
	once       sync.Once
	closeErr   error
}

// New recovers recognized abandoned owners and creates a private process directory
// under cacheDir/analysis. Unknown, malformed and actively locked entries survive.
func New(cacheDir string) (*Owner, error) {
	if cacheDir == "" {
		return nil, errors.New("empty analysis cache path")
	}
	path, err := filepath.Abs(filepath.Join(cacheDir, "analysis"))
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err = os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !plainDir(before) {
		return nil, fmt.Errorf("analysis cache root is not an ordinary directory: %s", path)
	}
	parent, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	info, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, info) {
		parent.Close()
		return nil, errIdentity
	}
	if err = recoverOwners(parent); err != nil {
		parent.Close()
		return nil, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		parent.Close()
		return nil, err
	}
	name := "process-" + hex.EncodeToString(random[:])
	if err = parent.Mkdir(name, 0700); err != nil {
		parent.Close()
		return nil, err
	}
	o, err := openOwner(parent, name)
	if err != nil { // Only the freshly created empty directory is ours.
		// Do not remove anything if the path was substituted while opening it.
		parent.Close()
		return nil, err
	}
	o.dir = filepath.Join(path, name)
	o.lock, err = openChild(o.directory, "owner.lock", true, true, false)
	if err == nil {
		o.lockInfo, err = o.lock.Stat()
	}
	if err == nil {
		var held bool
		held, err = tryLock(o.lock)
		if err == nil && !held {
			err = errors.New("new analysis owner lock is busy")
		}
	}
	if err == nil {
		var f *os.File
		f, err = openChild(o.directory, "owner.json", true, true, false)
		if err == nil {
			// Capture the exclusively created file before writing: even a partial
			// write (for example ENOSPC) must be eligible for our rollback.
			o.markerInfo, err = f.Stat()
			if err == nil {
				data, _ := json.Marshal(marker{application, 1, strings.TrimPrefix(name, "process-")})
				_, err = f.Write(data)
			}
			err = errors.Join(err, f.Close())
		}
	}
	if err != nil {
		cleanupErr := o.discardInitialization()
		return nil, errors.Join(err, cleanupErr)
	}
	return o, nil
}

// Dir is the parent for the engine's per-operation temporary directories.
func (o *Owner) Dir() string { return o.dir }

// Close removes only this owner's artifacts. It is concurrent and idempotent,
// including when cleanup fails: subsequent calls return the original result.
func (o *Owner) Close() error {
	o.once.Do(func() { o.closeErr = errors.Join(o.retire(), o.release(), o.parent.Close()) })
	return o.closeErr
}

func plainDir(info os.FileInfo) bool {
	return info.IsDir() && info.Mode()&os.ModeSymlink == 0 && !isReparse(info)
}
func plainFile(info os.FileInfo) bool { return info.Mode().IsRegular() && !isReparse(info) }

// openOwner checks both the native no-follow directory handle and the Root used
// for bounded removal. Root anchors deletion even if the directory is renamed.
func openOwner(parent *os.Root, name string) (*Owner, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !plainDir(info) {
		return nil, errIdentity
	}
	p, err := parent.Open(".")
	if err != nil {
		return nil, err
	}
	f, err := openChild(p, name, false, false, true)
	p.Close()
	if err != nil {
		return nil, err
	}
	current, err := f.Stat()
	if err != nil || !plainDir(current) || !os.SameFile(info, current) {
		f.Close()
		return nil, errIdentity
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		f.Close()
		return nil, err
	}
	current, err = root.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		root.Close()
		f.Close()
		return nil, errIdentity
	}
	return &Owner{name: name, parent: parent, root: root, directory: f, dirInfo: info}, nil
}

func ownName(name string) bool {
	if len(name) != 40 || !strings.HasPrefix(name, "process-") {
		return false
	}
	for _, c := range name[8:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func recoverOwners(parent *os.Root) error {
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	err = errors.Join(err, dir.Close())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !ownName(entry.Name()) {
			continue
		}
		o, err := openOwner(parent, entry.Name())
		if err != nil {
			continue
		}
		// Missing, malformed, or uninspectable ownership evidence is foreign.
		o.markerInfo, err = o.readMarker()
		if err != nil {
			o.release()
			continue
		}
		o.lock, err = o.openOrdinary("owner.lock", true)
		if err != nil {
			// A missing or replaced lock is no proof of abandonment. However,
			// inability to open a still-ordinary lock is an operational failure.
			info, statErr := o.root.Lstat("owner.lock")
			o.release()
			if statErr == nil && plainFile(info) && !errors.Is(err, errIdentity) {
				return fmt.Errorf("open abandoned analysis lock: %w", err)
			}
			continue
		}
		o.lockInfo, err = o.lock.Stat()
		if err != nil {
			o.release()
			continue
		}
		held, err := tryLock(o.lock)
		if err != nil {
			o.release()
			return fmt.Errorf("lock abandoned analysis scratch: %w", err)
		}
		if !held {
			o.release()
			continue
		}
		// A closer may have retired the marker while this opener was acquiring its
		// lock. No cleanup is authorized until every identity is checked again.
		if err = o.validate(); err != nil {
			o.release()
			continue
		}
		err = errors.Join(o.retire(), o.release())
		if err != nil {
			return fmt.Errorf("remove abandoned analysis scratch %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (o *Owner) openOrdinary(name string, write bool) (*os.File, error) {
	before, err := o.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !plainFile(before) {
		return nil, errIdentity
	}
	f, err := openChild(o.directory, name, write, false, false)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil || !plainFile(after) || !os.SameFile(before, after) {
		f.Close()
		return nil, errIdentity
	}
	return f, nil
}

func (o *Owner) readMarker() (os.FileInfo, error) {
	f, err := o.openOrdinary("owner.json", false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > markerLimit {
		return nil, errIdentity
	}
	data, err := io.ReadAll(io.LimitReader(f, markerLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > markerLimit {
		return nil, errIdentity
	}
	var m marker
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return nil, errIdentity
	}
	if d.Decode(new(any)) != io.EOF || m.Application != application || m.Version != 1 || m.OwnerID != strings.TrimPrefix(o.name, "process-") {
		return nil, errIdentity
	}
	return info, nil
}

func sameEntry(root *os.Root, name string, expected os.FileInfo) error {
	actual, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if expected == nil || isReparse(actual) || actual.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, actual) {
		return errIdentity
	}
	return nil
}

func (o *Owner) validate() error {
	if err := sameEntry(o.parent, o.name, o.dirInfo); err != nil {
		return err
	}
	if err := sameEntry(o.root, "owner.lock", o.lockInfo); err != nil {
		return err
	}
	info, err := o.readMarker()
	if err != nil {
		return err
	}
	if o.markerInfo == nil || !os.SameFile(o.markerInfo, info) {
		return errIdentity
	}
	return nil
}

func (o *Owner) retire() error {
	if err := o.validate(); err != nil {
		return err
	}
	f, err := o.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "owner.json" || entry.Name() == "owner.lock" {
			continue
		}
		if err = o.validate(); err != nil {
			return err
		}
		if err = o.root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	if err = o.validate(); err != nil {
		return err
	}
	// Invalidate recovery authority BEFORE releasing the lock. A concurrent scanner
	// that opened the old lock must re-read the now-missing marker after locking it.
	if err = o.root.Remove("owner.json"); err != nil {
		return err
	}
	return o.finishRetirement()
}

func (o *Owner) finishRetirement() error {
	// Windows cannot reliably delete open lock files. No new owner can be admitted
	// here: New always exclusively creates a fresh random directory, never adopts it.
	if o.lock != nil {
		err := o.lock.Close()
		o.lock = nil
		if err != nil {
			return err
		}
	}
	if err := sameEntry(o.parent, o.name, o.dirInfo); err != nil {
		return err
	}
	if o.lockInfo != nil {
		if err := sameEntry(o.root, "owner.lock", o.lockInfo); err != nil {
			return err
		}
		if err := o.root.Remove("owner.lock"); err != nil {
			return err
		}
	}
	if err := o.release(); err != nil {
		return err
	}
	if err := sameEntry(o.parent, o.name, o.dirInfo); err != nil {
		return err
	}
	return o.parent.Remove(o.name) // Empty only; never recursively delete this path.
}

func (o *Owner) discardInitialization() error {
	defer o.parent.Close()
	defer o.release()
	if err := sameEntry(o.parent, o.name, o.dirInfo); err != nil {
		return err
	}
	// No engine has seen Dir yet; only our exclusively created metadata may exist.
	if o.markerInfo != nil {
		if err := sameEntry(o.root, "owner.json", o.markerInfo); err != nil {
			return err
		}
		if err := o.root.Remove("owner.json"); err != nil {
			return err
		}
	}
	return o.finishRetirement()
}

func (o *Owner) release() error {
	var errs []error
	if o.lock != nil {
		errs = append(errs, o.lock.Close())
		o.lock = nil
	}
	if o.directory != nil {
		errs = append(errs, o.directory.Close())
		o.directory = nil
	}
	if o.root != nil {
		errs = append(errs, o.root.Close())
		o.root = nil
	}
	return errors.Join(errs...)
}
