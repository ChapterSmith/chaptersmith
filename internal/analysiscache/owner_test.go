package analysiscache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Catches shared directories, missing ownership records, and cleanup outside the owner.
func TestOwnerPrivateAndClose(t *testing.T) {
	cache := t.TempDir()
	sentinel := filepath.Join(cache, "caller")
	write(t, sentinel, "source bytes")
	a, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if a.Dir() == b.Dir() || filepath.Dir(a.Dir()) != filepath.Join(cache, "analysis") {
		t.Fatalf("nonprivate directories: %s %s", a.Dir(), b.Dir())
	}
	name := filepath.Base(a.Dir())
	if !regexp.MustCompile(`^process-[0-9a-f]{32}$`).MatchString(name) {
		t.Fatalf("bad name %q", name)
	}
	var marker struct {
		Application string `json:"application"`
		Version     int    `json:"version"`
		OwnerID     string `json:"ownerId"`
	}
	data, err := os.ReadFile(filepath.Join(a.Dir(), "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.Application != "audiobook-merger-analysis" || marker.Version != 1 || marker.OwnerID != strings.TrimPrefix(name, "process-") {
		t.Fatalf("bad marker: %+v", marker)
	}
	write(t, filepath.Join(a.Dir(), "pcm"), "tiny scratch")
	write(t, filepath.Join(b.Dir(), "sibling"), "keep")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	absent(t, a.Dir())
	contents(t, sentinel, "source bytes")
	contents(t, filepath.Join(b.Dir(), "sibling"), "keep")
}

// Exercises the actual OS lock in another process; no PID/mtime liveness substitute works.
func TestOwnerActiveAndAbandoned(t *testing.T) {
	cache := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnerHelperProcess$")
	cmd.Env = append(os.Environ(), "ANALYSISCACHE_HELPER="+cache)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
		} else {
			ready <- ""
		}
	}()
	var dir string
	select {
	case dir = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("helper startup timeout")
	}
	if filepath.Dir(dir) != filepath.Join(cache, "analysis") {
		t.Fatalf("helper not ready: %q", dir)
	}
	live, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	contents(t, filepath.Join(dir, "pcm"), "helper scratch")
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	stopped = true
	recovered, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	absent(t, dir)
}

func TestOwnerHelperProcess(t *testing.T) {
	cache := os.Getenv("ANALYSISCACHE_HELPER")
	if cache == "" {
		return
	}
	owner, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(owner.Dir(), "pcm"), "helper scratch")
	fmt.Println(owner.Dir())
	for {
		time.Sleep(time.Hour)
	} // Parent kills only this child; intentionally no Close/defer.
}

// Catches deletion on name alone, unbounded/trailing markers, and symlink traversal.
func TestOwnerRejectsForeign(t *testing.T) {
	for _, kind := range []string{"unknown-name", "malformed", "wrong-app", "wrong-version", "wrong-id", "oversized", "trailing", "missing-marker", "missing-lock", "symlink-dir", "symlink-marker", "symlink-lock"} {
		t.Run(kind, func(t *testing.T) {
			cache := t.TempDir()
			root := filepath.Join(cache, "analysis")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 32)
			name := "process-" + id
			if kind == "unknown-name" {
				name = "other"
			}
			dir := filepath.Join(root, name)
			source := t.TempDir()
			write(t, filepath.Join(source, "source"), "original")
			if kind == "symlink-dir" {
				symlink(t, source, dir)
			} else {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			marker := fmt.Sprintf(`{"application":"audiobook-merger-analysis","version":1,"ownerId":%q}`, id)
			switch kind {
			case "malformed":
				marker = "{"
			case "wrong-app":
				marker = strings.ReplaceAll(marker, "audiobook-merger-analysis", "foreign")
			case "wrong-version":
				marker = strings.ReplaceAll(marker, `"version":1`, `"version":2`)
			case "wrong-id":
				marker = strings.ReplaceAll(marker, id, strings.Repeat("b", 32))
			case "oversized":
				marker += strings.Repeat(" ", 8192)
			case "trailing":
				marker += "{}"
			}
			if kind != "missing-marker" {
				if kind == "symlink-marker" {
					write(t, filepath.Join(source, "marker"), marker)
					symlink(t, filepath.Join(source, "marker"), filepath.Join(dir, "owner.json"))
				} else {
					write(t, filepath.Join(dir, "owner.json"), marker)
				}
			}
			if kind != "missing-lock" {
				if kind == "symlink-lock" {
					write(t, filepath.Join(source, "lock"), "keep")
					symlink(t, filepath.Join(source, "lock"), filepath.Join(dir, "owner.lock"))
				} else {
					write(t, filepath.Join(dir, "owner.lock"), "")
				}
			}
			write(t, filepath.Join(dir, "keep"), "foreign scratch")
			owner, err := New(cache)
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			contents(t, filepath.Join(dir, "keep"), "foreign scratch")
			contents(t, filepath.Join(source, "source"), "original")
		})
	}
}

// Catches Close following a replaced process path, marker or lock to unrelated data.
func TestOwnerIdentityReplacement(t *testing.T) {
	for _, part := range []string{"directory", "owner.json", "owner.lock"} {
		t.Run(part, func(t *testing.T) {
			if part == "directory" && runtime.GOOS == "windows" {
				t.Skip("Windows refuses to rename a directory that holds open files")
			}
			cache := t.TempDir()
			owner, err := New(cache)
			if err != nil {
				t.Fatal(err)
			}
			path := owner.Dir()
			if part != "directory" {
				path = filepath.Join(path, part)
			}
			if err := os.Rename(path, path+"-moved"); err != nil {
				t.Fatal(err)
			}
			if part == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(path, "keep"), "replacement")
			} else {
				write(t, path, "replacement")
			}
			first := owner.Close()
			if first == nil {
				t.Fatal("replacement must produce error")
			}
			if owner.Close() != first {
				t.Fatal("Close did not retain result")
			}
			if part == "directory" {
				contents(t, filepath.Join(path, "keep"), "replacement")
			} else {
				contents(t, path, "replacement")
			}
		})
	}
}

func TestOwnerInitializationPreservesCaller(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			cache := t.TempDir()
			source := t.TempDir()
			write(t, filepath.Join(source, "source"), "original")
			target := filepath.Join(cache, "analysis")
			if kind == "file" {
				write(t, target, "caller")
			} else {
				symlink(t, source, target)
			}
			if owner, err := New(cache); err == nil {
				owner.Close()
				t.Fatal("expected invalid root error")
			}
			contents(t, filepath.Join(source, "source"), "original")
			if kind == "file" {
				contents(t, target, "caller")
			}
		})
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func contents(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("%s = %q, %v; want %q", path, b, err, want)
	}
}
func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s remains: %v", path, err)
	}
}
func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

// Catches simultaneous recovery deleting another initializer or live owner.
func TestOwnerConcurrentRecovery(t *testing.T) {
	cache := t.TempDir()
	abandoned, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(abandoned.Dir(), "scratch"), "abandoned")
	// The subprocess test independently proves OS-exit lock release. Here release
	// only simulates the abandoned state to coordinate several scanners cheaply.
	if err := abandoned.release(); err != nil {
		t.Fatal(err)
	}
	if err := abandoned.parent.Close(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	owners := make(chan *Owner, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			o, err := New(cache)
			if err != nil {
				errs <- err
				return
			}
			owners <- o
		}()
	}
	close(start)
	wg.Wait()
	close(owners)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var live []*Owner
	for o := range owners {
		live = append(live, o)
		write(t, filepath.Join(o.Dir(), "live"), "alive")
	}
	for _, o := range live {
		contents(t, filepath.Join(o.Dir(), "live"), "alive")
		if err := o.Close(); err != nil {
			t.Error(err)
		}
	}
	absent(t, abandoned.Dir())
}

func TestOwnerScratchSymlinkPreservesSource(t *testing.T) {
	cache := t.TempDir()
	source := t.TempDir()
	write(t, filepath.Join(source, "audio"), "original bytes")
	owner, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	symlink(t, source, filepath.Join(owner.Dir(), "scratch-link"))
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	contents(t, filepath.Join(source, "audio"), "original bytes")
	absent(t, owner.Dir())
}
