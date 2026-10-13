package analysis

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSourceContentChanged(t *testing.T) {
	s := testSource(t, "a", 1)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}}, Channels: 1}
	before, e := os.ReadFile(s.Path)
	if e != nil {
		t.Fatal(e)
	}
	r, e := Analyze(context.Background(), in, Options{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(s.Path)
	if string(before) != string(after) {
		t.Fatal("source mutated")
	}
	st, _ := os.Stat(s.Path)
	after[len(after)-1] ^= 1
	if e = os.WriteFile(s.Path, after, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(s.Path, st.ModTime(), st.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = CheckSources(context.Background(), in, r.Sources); e == nil {
		t.Fatal("same-size same-mtime mutation accepted")
	}
}
func TestSourceIdentityReplacement(t *testing.T) {
	s := testSource(t, "a", 1)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}}, Channels: 1}
	r, e := Analyze(context.Background(), in, Options{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(s.Path)
	st, _ := os.Stat(s.Path)
	replacement := s.Path + ".replacement"
	if e = os.WriteFile(replacement, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(replacement, st.ModTime(), st.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(replacement, s.Path); e != nil {
		t.Fatal(e)
	}
	if e = CheckSources(context.Background(), in, r.Sources); e == nil {
		t.Fatal("same-content replacement accepted")
	}
}
func TestSourceMutationDuringAnalysis(t *testing.T) {
	s := testSource(t, "a", 1)
	in := Input{Sources: []Source{s}, Spans: []Span{{SourceID: "a", EndUS: 1000000}}, Channels: 1}
	mutated := false
	_, e := Analyze(context.Background(), in, Options{}, func(p Progress) {
		if p.Phase == "decode" && !mutated {
			mutated = true
			st, _ := os.Stat(s.Path)
			b, _ := os.ReadFile(s.Path)
			b[len(b)-1] ^= 1
			if e := os.WriteFile(s.Path, b, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.Chtimes(s.Path, st.ModTime(), st.ModTime()); e != nil {
				t.Fatal(e)
			}
		}
	})
	if e == nil || !mutated {
		t.Fatal("post-decode hash failed to catch modification", e)
	}
}

type controlledSourceReader struct {
	sourceReader
	cancel context.CancelFunc
	fault  error
	closed bool
}

func (r *controlledSourceReader) Read(b []byte) (int, error) {
	if r.cancel != nil {
		r.cancel()
	}
	if r.fault != nil {
		return 0, r.fault
	}
	return r.sourceReader.Read(b)
}
func (r *controlledSourceReader) Close() error { r.closed = true; return r.sourceReader.Close() }
func TestSourceHashCancellationAndReadError(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		s := testSource(t, "a", 2)
		ctx, cancel := context.WithCancel(context.Background())
		file, e := os.Open(s.Path)
		if e != nil {
			t.Fatal(e)
		}
		fault := errors.New("injected hash IO error")
		r := &controlledSourceReader{sourceReader: file, fault: fault}
		if cancelRead {
			r.cancel = cancel
			r.fault = nil
			fault = context.Canceled
		}
		_, e = sourceHash(ctx, s, func(string) (sourceReader, error) { return r, nil })
		cancel()
		if !errors.Is(e, fault) || !r.closed {
			t.Fatalf("closed %v error %v", r.closed, e)
		}
	}
}
