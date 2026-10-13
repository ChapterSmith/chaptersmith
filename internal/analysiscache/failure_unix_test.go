//go:build darwin || linux

package analysiscache

import (
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A recognized scratch cleanup failure must be visible, with its lock and marker
// still available for a subsequent recovery once permissions are repaired.
func TestOwnerCleanupFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not constrain root")
	}
	cache := t.TempDir()
	owner, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(owner.Dir(), "operation")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(blocked, "pcm"), "small scratch")
	if err := os.Chmod(blocked, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0700)
	first := owner.Close()
	if first == nil {
		t.Fatal("cleanup failure hidden")
	}
	if owner.Close() != first {
		t.Fatal("cleanup result not retained")
	}
	if another, err := New(cache); err == nil {
		another.Close()
		t.Fatal("recovery failure hidden")
	}
	contents(t, filepath.Join(blocked, "pcm"), "small scratch")
	if err := os.Chmod(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	absent(t, owner.Dir())
}

func TestOwnerInitializationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not constrain root")
	}
	cache := t.TempDir()
	root := filepath.Join(cache, "analysis")
	if err := os.Mkdir(root, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0700)
	write(t, filepath.Join(cache, "caller"), "source")
	if owner, err := New(cache); err == nil {
		owner.Close()
		t.Fatal("initialization failure hidden")
	}
	contents(t, filepath.Join(cache, "caller"), "source")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("initialization debris: %v", entries)
	}
}

// Named-pipe metadata must not hang an opener or become cleanup authority.
func TestOwnerRejectsSpecialMetadata(t *testing.T) {
	cache := t.TempDir()
	dir := filepath.Join(cache, "analysis", "process-"+strings.Repeat("d", 32))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "keep"), "source")
	if err := unix.Mkfifo(filepath.Join(dir, "owner.json"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	contents(t, filepath.Join(dir, "keep"), "source")
}

// A real per-process file-size limit forces a partial marker write without
// exhausting disk or altering the parent process. Its newly created files must
// be removed even though the marker never became valid.
func TestOwnerPartialInitialization(t *testing.T) {
	if cache := os.Getenv("ANALYSISCACHE_FAILWRITE"); cache != "" {
		signal.Ignore(syscall.SIGXFSZ)
		var limit unix.Rlimit
		if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &limit); err != nil {
			t.Fatal(err)
		}
		limit.Cur = 1
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &limit); err != nil {
			t.Fatal(err)
		}
		if owner, err := New(cache); err == nil {
			owner.Close()
			t.Fatal("expected marker write failure")
		}
		entries, err := os.ReadDir(filepath.Join(cache, "analysis"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("partial initialization leaked artifacts: %v", entries)
		}
		return
	}
	cache := t.TempDir()
	write(t, filepath.Join(cache, "caller"), "source bytes")
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnerPartialInitialization$")
	cmd.Env = append(os.Environ(), "ANALYSISCACHE_FAILWRITE="+cache)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("limited helper: %v\n%s", err, out)
	}
	contents(t, filepath.Join(cache, "caller"), "source bytes")
}

func TestOwnerRecoveryLockFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not constrain root")
	}
	cache := t.TempDir()
	owner, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.release(); err != nil {
		t.Fatal(err)
	}
	if err := owner.parent.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(owner.Dir(), "owner.lock")
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if recovered, err := New(cache); err == nil {
		recovered.Close()
		t.Fatal("recognized owner lock failure hidden")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(cache)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	absent(t, owner.Dir())
}
