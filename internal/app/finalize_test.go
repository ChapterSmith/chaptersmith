package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chaptersmith/internal/audiotest"
)

func TestRunFinalizationFailureNoSuccess(t *testing.T) {
	for _, kind := range []string{"publish", "cancel", "cover"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.m4a")
			audiotest.WriteAAC(t, source, 22050, 0)
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			cfg := cliConfig(dir)
			cfg.OutputPath = filepath.Join(dir, "out.tmp")
			if kind == "publish" {
				if err := os.Mkdir(cfg.OutputPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "cover" {
				writeCLIFile(t, filepath.Join(dir, "cover.jpg"), "corrupt image")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancel" {
				cfg.Progress.OnPhase = func(s string) {
					if s == "FINALIZING AUDIOBOOK" {
						cancel()
					}
				}
			}
			var logs []string
			cfg.Progress.OnLog = func(s string) { logs = append(logs, s) }
			complete := false
			cfg.Progress.OnProgress = func(_, _ int, s string) {
				if s == "Complete" {
					complete = true
				}
			}
			err = New(cfg, log.New(io.Discard, "", 0)).Run(ctx)
			if err == nil || complete || strings.Contains(strings.Join(logs, "\n"), "SUCCESS!") {
				t.Fatalf("false success: error=%v complete=%t logs=%v", err, complete, logs)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
			after, err := os.ReadFile(source)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("source changed", err)
			}
		})
	}
}

func TestRunReportsActualFinalPath(t *testing.T) {
	for _, fix := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "fix"}[fix], func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.m4a")
			audiotest.WriteAAC(t, source, 22050, 0)
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			cfg := cliConfig(dir)
			cfg.FixMode = fix
			cfg.OutputPath = filepath.Join(dir, "out.tmp")
			var logs []string
			cfg.Progress.OnLog = func(s string) { logs = append(logs, s) }
			complete := false
			cfg.Progress.OnProgress = func(_, _ int, s string) {
				if s == "Complete" {
					complete = true
				}
			}
			if err := runCLI(cfg); err != nil {
				t.Fatal(err)
			}
			actual := cfg.OutputPath
			joined := strings.Join(logs, "\n")
			if !complete || !strings.Contains(joined, "created: '"+actual+"'") || strings.Contains(joined, "File size:  - Duration:") {
				t.Fatalf("final path/statistics: %s", joined)
			}
			if _, err := os.Stat(actual); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(dir, "out.m4b")); !os.IsNotExist(err) {
				t.Fatalf("unexpected renamed output: %v", err)
			}
			after, err := os.ReadFile(source)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("source changed", err)
			}
		})
	}
}
