package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const repeatHop int64 = 11025
const repeatWindow int64 = 88200
const repeatLimitations = "repeat_method: exact quantized PCM16 only; may miss gain changes, separately encoded near-duplicates, repeats shorter than 8 seconds, and offsets outside the 250 ms grid. Matches can be deliberate music, introductions or refrains. Coalesced pairs stay nonoverlapping and at most 60 seconds; residual tails shorter than 8 seconds are omitted."

type repeatLimits struct {
	pcmBytes, indexBytes int64
	pairs, occurrences   int
}

func defaultRepeatLimits() repeatLimits {
	return repeatLimits{MaxPCMBytes, MaxIndexBytes, MaxPairs, MaxOccurrences}
}

type repeatPCM interface {
	Write([]byte) (int, error)
	ReadAt([]byte, int64) (int, error)
	Close() error
}
type repeatScanner struct {
	ctx                                                           context.Context
	dir                                                           string
	channels                                                      int
	limits                                                        repeatLimits
	db                                                            *sql.DB
	pcm                                                           repeatPCM
	digest                                                        func([]byte, int) [32]byte
	rolling, quant, verifyA, verifyB                              []byte
	nextWindow, bufferStart, position, spoolFrames, scannedFrames int64
	segmentStart, segmentSpool                                    int64
	segmentActive                                                 bool
	capturing, complete, closed                                   bool
	maxPages                                                      int64
	limitations                                                   []string
}

func repeatDigest(b []byte, channels int) [32]byte {
	h := sha256.New()
	h.Write([]byte{byte(channels)})
	h.Write(b)
	var d [32]byte
	copy(d[:], h.Sum(nil))
	return d
}
func newRepeatScanner(ctx context.Context, dir string, channels int, limits repeatLimits) (s *repeatScanner, err error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if channels != 1 && channels != 2 {
		return nil, errors.New("repeat channels must be one or two")
	}
	if limits.pcmBytes < 0 || limits.indexBytes < 0 || limits.pairs < 0 || limits.pairs > MaxPairs || limits.occurrences < 1 || limits.occurrences > MaxOccurrences || limits.pcmBytes > MaxPCMBytes || limits.indexBytes > MaxIndexBytes {
		return nil, errors.New("invalid repeat limits")
	}
	owned, e := os.MkdirTemp(dir, "analysis-repeat-")
	if e != nil {
		return nil, e
	}
	s = &repeatScanner{ctx: ctx, dir: owned, channels: channels, limits: limits, digest: repeatDigest, capturing: true, complete: true, limitations: []string{repeatLimitations}}
	ownedScanner := s
	defer func() {
		if err != nil {
			err = errors.Join(err, ownedScanner.Close())
		}
	}()
	// DELETE journals can contain every original DB page. Reserve twice the DB
	// ceiling for journal records/sector padding, plus 64 KiB fixed overhead.
	// There is no WAL. All SQL uses primary-key order, never a temporary sort.
	s.maxPages = max(int64(0), (limits.indexBytes-(64<<10))/3/4096)
	if s.maxPages < 4 {
		s.limit("repeat_index_limit")
		return s, nil
	}
	dbPath, e := filepath.Abs(filepath.Join(owned, "index.sqlite"))
	if e != nil {
		return nil, e
	}
	// A drive-letter path needs a leading slash (file:///D:/...); without it
	// the drive would be parsed as the URI authority and rejected.
	uriPath := filepath.ToSlash(dbPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dbURI := url.URL{Scheme: "file", Path: uriPath}
	s.db, err = sql.Open("sqlite", dbURI.String())
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	// The index is process scratch that Close deletes, so it trades durability
	// for speed: a DELETE journal with full sync costs a file create, fsyncs and
	// an unlink per seed insert. The journal stays on disk, inside the budget.
	for _, q := range []string{"PRAGMA page_size=4096", "PRAGMA journal_mode=PERSIST", "PRAGMA synchronous=OFF", "PRAGMA cache_size=-8192", "PRAGMA cache_spill=OFF", "PRAGMA temp_store=FILE", "PRAGMA mmap_size=0", fmt.Sprintf("PRAGMA max_page_count=%d", s.maxPages), "CREATE TABLE seeds (digest BLOB NOT NULL, start INTEGER NOT NULL, spool INTEGER NOT NULL, PRIMARY KEY(digest,start)) WITHOUT ROWID", "CREATE TABLE segments (start INTEGER PRIMARY KEY, end INTEGER NOT NULL)"} {
		if _, err = s.db.ExecContext(ctx, q); err != nil {
			return nil, err
		}
	}
	s.pcm, err = os.OpenFile(filepath.Join(owned, "pcm.s16le"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	frameBytes := channels * 2
	s.rolling = make([]byte, 0, (int(repeatWindow)+16384)*frameBytes)
	s.quant = make([]byte, 16384*frameBytes)
	s.verifyA = make([]byte, int(repeatHop)*frameBytes)
	s.verifyB = make([]byte, len(s.verifyA))
	return s, nil
}
func (s *repeatScanner) limit(code string) {
	s.complete = false
	for _, l := range s.limitations {
		if l == code {
			return
		}
	}
	s.limitations = append(s.limitations, code)
	if code == "repeat_pcm_limit" || code == "repeat_index_limit" {
		s.capturing = false
	}
}
func (s *repeatScanner) bufferBytes() int64 {
	return int64(cap(s.rolling) + cap(s.quant) + cap(s.verifyA) + cap(s.verifyB))
}
func (s *repeatScanner) indexRoom() (bool, error) {
	var pages int64
	if e := s.db.QueryRowContext(s.ctx, "PRAGMA page_count").Scan(&pages); e != nil {
		return false, e
	}
	return pages+8 <= s.maxPages, nil
}
func (s *repeatScanner) endSegment() error {
	if !s.segmentActive {
		return nil
	}
	// Eight pages were reserved before admitting a seed, sufficient for this
	// single-row mapping insertion including a B-tree root split.
	_, e := s.db.ExecContext(s.ctx, "INSERT INTO segments(start,end) VALUES(?,?)", s.segmentStart, s.position)
	s.segmentActive = false
	return e
}
func (s *repeatScanner) Push(samples []float32, outputStartFrame int64) error {
	if e := s.ctx.Err(); e != nil {
		return e
	}
	if s.closed {
		return errors.New("repeat scanner closed")
	}
	if outputStartFrame != s.position || len(samples)%s.channels != 0 || len(samples)/s.channels > 16384 {
		return errors.New("invalid repeat PCM position or chunk")
	}
	if !s.capturing {
		s.position += int64(len(samples) / s.channels)
		return nil
	}
	frameBytes := s.channels * 2
	frames := int64(len(samples) / s.channels)
	admitted := min(frames, (s.limits.pcmBytes-s.spoolFrames*int64(frameBytes))/int64(frameBytes))
	if admitted > 0 {
		if !s.segmentActive {
			room, e := s.indexRoom()
			if e != nil {
				return e
			}
			if !room {
				s.limit("repeat_index_limit")
				s.position += frames
				return nil
			}
			s.segmentActive = true
			s.segmentStart = s.position
			s.segmentSpool = s.spoolFrames
			s.nextWindow = ((s.position + repeatHop - 1) / repeatHop) * repeatHop
			s.bufferStart = s.position
			s.rolling = s.rolling[:0]
		}
		b := s.quant[:int(admitted)*frameBytes]
		for i, v := range samples[:int(admitted)*s.channels] {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return errors.New("nonfinite repeat sample")
			}
			q := int16(math.Round(max(-1, min(1, float64(v))) * 32767))
			binary.LittleEndian.PutUint16(b[i*2:], uint16(q))
		}
		n, e := s.pcm.Write(b)
		if e != nil {
			return fmt.Errorf("repeat PCM scratch: %w", e)
		}
		if n != len(b) {
			return io.ErrShortWrite
		}
		s.spoolFrames += admitted
		s.scannedFrames += admitted
		s.position += admitted
		s.rolling = append(s.rolling, b...)
		for s.nextWindow+repeatWindow <= s.position {
			if e := s.ctx.Err(); e != nil {
				return e
			}
			begin := int(s.nextWindow-s.bufferStart) * frameBytes
			window := s.rolling[begin : begin+int(repeatWindow)*frameBytes]
			if repeatAudible(window) {
				room, e := s.indexRoom()
				if e != nil {
					return e
				}
				if !room {
					s.limit("repeat_index_limit")
					break
				}
				digest := s.digest(window, s.channels)
				_, e = s.db.ExecContext(s.ctx, "INSERT INTO seeds(digest,start,spool) VALUES(?,?,?)", digest[:], s.nextWindow, s.segmentSpool+s.nextWindow-s.segmentStart)
				if e != nil {
					return fmt.Errorf("repeat index: %w", e)
				}
			}
			s.nextWindow += repeatHop
		}
		// Drop samples preceding the next absolute-grid window, including the
		// partial hop after a synthetic gap. Capacity stays fixed across duration.
		drop := min(s.nextWindow, s.position) - s.bufferStart
		if drop > 0 {
			copy(s.rolling, s.rolling[int(drop)*frameBytes:])
			s.rolling = s.rolling[:len(s.rolling)-int(drop)*frameBytes]
			s.bufferStart += drop
		}
	}
	if admitted < frames {
		s.limit("repeat_pcm_limit")
	}
	if !s.capturing {
		if e := s.endSegment(); e != nil {
			return e
		}
		s.rolling = s.rolling[:0]
	}
	s.position += frames - admitted
	return nil
}
func repeatAudible(b []byte) bool {
	var sum, square float64
	for i := 0; i < len(b); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(b[i:]))) / 32767
		sum += v
		square += v * v
	}
	n := float64(len(b) / 2)
	mean := sum / n
	return square/n > math.Pow(10, -4.5) && square/n-mean*mean > 0.000001
}
func (s *repeatScanner) Break(outputEndFrame int64) error {
	if e := s.ctx.Err(); e != nil {
		return e
	}
	if outputEndFrame < s.position {
		return errors.New("repeat break goes backwards")
	}
	if e := s.endSegment(); e != nil {
		return e
	}
	s.position = outputEndFrame
	s.rolling = s.rolling[:0]
	return nil
}

type repeatOccurrence struct{ start, spool, segmentStart, segmentEnd int64 }
type repeatMatch struct{ a, b, end int64 }

func (s *repeatScanner) equal(a, b repeatOccurrence, relative int64) (bool, error) {
	if e := s.ctx.Err(); e != nil {
		return false, e
	}
	stride := int64(s.channels * 2)
	if _, e := s.pcm.ReadAt(s.verifyA, (a.spool+relative)*stride); e != nil {
		return false, e
	}
	if _, e := s.pcm.ReadAt(s.verifyB, (b.spool+relative)*stride); e != nil {
		return false, e
	}
	return bytes.Equal(s.verifyA, s.verifyB), nil
}
func (s *repeatScanner) verify(a, b repeatOccurrence) (repeatMatch, bool, error) {
	// First verify the seed, then extend both ways on the same absolute grid.
	// Backwards extension is needed because digest ordering is not time ordering.
	offset := b.start - a.start
	low := int64(0)
	high := int64(0)
	for high < 60*44100 && a.start+high+repeatHop <= min(a.segmentEnd, b.start) && b.start+high+repeatHop <= b.segmentEnd {
		equal, e := s.equal(a, b, high)
		if e != nil {
			return repeatMatch{}, false, e
		}
		if !equal {
			break
		}
		high += repeatHop
	}
	for high-low < 60*44100 && a.start+low-repeatHop >= a.segmentStart && b.start+low-repeatHop >= b.segmentStart && high-low+repeatHop <= offset {
		equal, e := s.equal(a, b, low-repeatHop)
		if e != nil {
			return repeatMatch{}, false, e
		}
		if !equal {
			break
		}
		low -= repeatHop
	}
	return repeatMatch{a.start + low, b.start + low, a.start + high}, high-low >= 8*44100, nil
}
func (s *repeatScanner) Finish() ([]Finding, Coverage, []string, error) {
	coverage := func() Coverage { return Coverage{"repetition", s.complete, usAtFrame(s.scannedFrames)} }
	if e := s.ctx.Err(); e != nil {
		return nil, coverage(), s.limitations, e
	}
	if e := s.endSegment(); e != nil {
		return nil, coverage(), s.limitations, e
	}
	if s.db == nil {
		return nil, coverage(), s.limitations, nil
	}
	rows, e := s.db.QueryContext(s.ctx, `SELECT digest,start,spool,
 (SELECT start FROM segments WHERE start<=seeds.start ORDER BY start DESC LIMIT 1),
 (SELECT end FROM segments WHERE start<=seeds.start ORDER BY start DESC LIMIT 1)
 FROM seeds ORDER BY digest,start`)
	if e != nil {
		return nil, coverage(), s.limitations, e
	}
	defer rows.Close()
	var group []repeatOccurrence
	var previous [32]byte
	haveGroup := false
	pairs := map[[2]int64]bool{}
	matches := make([]repeatMatch, 0)
	process := func() error {
		for i, a := range group {
			for _, b := range group[i+1:] {
				if b.start-a.start < 8*44100 {
					continue
				}
				key := [2]int64{a.start, b.start}
				if pairs[key] {
					continue
				}
				if len(pairs) >= s.limits.pairs {
					s.limit("repeat_pair_limit")
					continue
				}
				pairs[key] = true
				m, ok, e := s.verify(a, b)
				if e != nil {
					return e
				}
				if ok {
					matches = append(matches, m)
				}
			}
		}
		return nil
	}
	for rows.Next() {
		if e = s.ctx.Err(); e != nil {
			return nil, coverage(), s.limitations, e
		}
		var digest []byte
		var o repeatOccurrence
		if e = rows.Scan(&digest, &o.start, &o.spool, &o.segmentStart, &o.segmentEnd); e != nil {
			return nil, coverage(), s.limitations, e
		}
		var key [32]byte
		copy(key[:], digest)
		if haveGroup && key != previous {
			if e = process(); e != nil {
				return nil, coverage(), s.limitations, e
			}
			group = group[:0]
		}
		previous = key
		haveGroup = true
		if len(group) < s.limits.occurrences {
			group = append(group, o)
		} else {
			s.limit("repeat_occurrence_limit")
		}
	}
	if e = rows.Err(); e != nil {
		return nil, coverage(), s.limitations, e
	}
	if e = process(); e != nil {
		return nil, coverage(), s.limitations, e
	}
	// Same-offset intersections describe the same candidate. Sort by offset and
	// earliest pair before merging, independent of digest or SQLite page layout.
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.b-a.a != b.b-b.a {
			return a.b-a.a < b.b-b.a
		}
		if a.a != b.a {
			return a.a < b.a
		}
		return a.end > b.end
	})
	merged := matches[:0]
	for _, m := range matches {
		if len(merged) > 0 {
			p := &merged[len(merged)-1]
			if p.b-p.a == m.b-m.a && m.a < p.end {
				p.end = max(p.end, m.end)
				continue
			}
		}
		merged = append(merged, m)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].a != merged[j].a {
			return merged[i].a < merged[j].a
		}
		return merged[i].b < merged[j].b
	})
	// Partition only the verified union; each returned pair is bounded by its
	// offset and 60 seconds. The earliest pair wins, and sub-eight-second
	// residuals are conservatively omitted (declared in the method limitation).
	bounded := make([]repeatMatch, 0, len(merged))
	for _, m := range merged {
		offset := m.b - m.a
		for a := m.a; a < m.end; {
			end := min(m.end, a+min(int64(60*44100), offset))
			if end-a >= 8*44100 {
				bounded = append(bounded, repeatMatch{a, a + offset, end})
			}
			a = end
		}
	}
	sort.Slice(bounded, func(i, j int) bool {
		if bounded[i].a != bounded[j].a {
			return bounded[i].a < bounded[j].a
		}
		return bounded[i].b < bounded[j].b
	})
	findings := make([]Finding, 0, len(bounded))
	for _, m := range bounded {
		a, end, b, bEnd := usAtFrame(m.a), usAtFrame(m.end), usAtFrame(m.b), usAtFrame(m.b+m.end-m.a)
		findings = append(findings, Finding{Kind: "repetition", Severity: "info", Verification: "pcm16-exact", Detail: "Byte-identical diagnostic PCM16 in two nonoverlapping intervals (at least 8 seconds). Listen to decide whether the repetition is intentional.", StartUS: &a, EndUS: &end, SecondStartUS: &b, SecondEndUS: &bEnd})
	}
	return findings, coverage(), s.limitations, nil
}
func (s *repeatScanner) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.db != nil {
		err = errors.Join(err, s.db.Close())
	}
	if s.pcm != nil {
		err = errors.Join(err, s.pcm.Close())
	}
	err = errors.Join(err, os.RemoveAll(s.dir))
	return err
}
