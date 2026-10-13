package app

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Omitting either selected sidecar from source protection used to truncate or
// rename it during finalization. Check both write names through the real CLI.
func TestCLIOutputAliasesSelectedSidecar(t *testing.T) {
	for _, fix := range []bool{false, true} {
		for _, ext := range []string{"cue", "m3u", "m3u8"} {
			for _, selection := range []string{"auto", "explicit"} {
				for _, alias := range []string{"direct", "symlink-write", "hardlink-write"} {
					t.Run(fmt.Sprintf("fix=%v/%s/%s/%s", fix, ext, selection, alias), func(t *testing.T) {
						fixtures := []string{"mp3/01.mp3", "cover.jpg"}
						if !fix {
							fixtures = append(fixtures, "mp3/02.mp3")
						}
						dir := copyFixtures(t, fixtures...)
						source := filepath.Join(dir, "01.mp3")
						content := source + "\n"
						if ext == "cue" {
							content = fmt.Sprintf("FILE %q WAVE\n TRACK 01 AUDIO\n INDEX 01 00:00:00\n", source)
						}
						if !fix {
							second := filepath.Join(dir, "02.mp3")
							if ext == "cue" {
								content += fmt.Sprintf("FILE %q WAVE\n TRACK 02 AUDIO\n INDEX 01 00:00:00\n", second)
							} else {
								content += second + "\n"
							}
						}
						sidecarDir := dir
						if selection == "explicit" {
							sidecarDir = t.TempDir()
						}
						sidecar := filepath.Join(sidecarDir, "book."+ext)
						writeCLIFile(t, sidecar, content)
						cfg := cliConfig(dir)
						cfg.FixMode = fix
						if selection == "explicit" {
							if ext == "cue" {
								cfg.CuePath = sidecar
							} else {
								cfg.PlaylistPath = sidecar
							}
						}
						cfg.OutputPath = sidecar
						protected := []string{source, filepath.Join(dir, "cover.jpg"), sidecar}
						if !fix {
							protected = append(protected, filepath.Join(dir, "02.mp3"))
						}
						if alias != "direct" {
							destination := filepath.Join(dir, "alias.m4b")
							if strings.HasPrefix(alias, "symlink") {
								if err := os.Symlink(sidecar, destination); err != nil {
									t.Skipf("symlink unsupported: %v", err)
								}
							} else if err := os.Link(sidecar, destination); err != nil {
								t.Fatal(err)
							}
							cfg.OutputPath = destination
							protected = append(protected, destination)
							if strings.HasSuffix(alias, "rename") {
								cfg.OutputPath = filepath.Join(dir, "alias.tmp")
								writeCLIFile(t, cfg.OutputPath, "pre-existing output must survive rejection")
								protected = append(protected, cfg.OutputPath)
							}
						}
						hashes := make(map[string][32]byte)
						identities := make(map[string]os.FileInfo)
						for _, path := range protected {
							b, err := os.ReadFile(path)
							if err != nil {
								t.Fatal(err)
							}
							hashes[path] = sha256.Sum256(b)
							identities[path], err = os.Lstat(path)
							if err != nil {
								t.Fatal(err)
							}
						}
						var phases []string
						cfg.Progress.OnPhase = func(phase string) { phases = append(phases, phase) }
						if err := runCLI(cfg); err == nil || !strings.Contains(err.Error(), "aliases source") {
							t.Errorf("expected selected-sidecar alias rejection: %v", err)
						}
						for _, path := range protected {
							b, err := os.ReadFile(path)
							if err != nil || sha256.Sum256(b) != hashes[path] {
								t.Errorf("input/destination bytes changed at %s: %v", path, err)
							}
							if !fix && selection == "auto" && alias == "direct" {
								t.Logf("%s SHA-256 before=%x after=%x", filepath.Base(path), hashes[path], sha256.Sum256(b))
							}
							after, err := os.Lstat(path)
							if err != nil || !os.SameFile(identities[path], after) {
								t.Errorf("input/destination name replaced at %s: %v", path, err)
							}
						}
						for _, phase := range phases {
							if strings.Contains(phase, "CONVERT") || strings.Contains(phase, "CONVERSION") || strings.Contains(phase, "FINALIZ") {
								t.Errorf("processed audio before rejecting collision: %s", phase)
							}
						}
					})
				}
			}
		}
	}
}
