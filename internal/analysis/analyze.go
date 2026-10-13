package analysis

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"chaptersmith/internal/audio"
	"github.com/google/uuid"
)

// At most 16384 compact keys are retained independently of the 200 display
// slots. Their bounded map occupies only a few MiB of the 32 MiB Go budget.
const maxObservationKeys = 16384

type dependencies struct {
	inspect         func(context.Context, string, int, int64, int64, func([]float32) error, func(audio.PCMProblem)) (audio.PCMInspection, error)
	open            openSource
	maxFrames       int64
	maxObservations int
	repeatLimits    repeatLimits
}

func defaultDependencies() dependencies {
	return dependencies{inspect: audio.InspectPCM, open: func(p string) (sourceReader, error) { return os.Open(p) }, maxFrames: 24 * 60 * 60 * 44100, maxObservations: maxObservationKeys, repeatLimits: defaultRepeatLimits()}
}
func frameAtUS(us int64) (int64, error) {
	if us < 0 || us > (math.MaxInt64-500000)/44100 {
		return 0, errors.New("audio time overflow")
	}
	return (us*44100 + 500000) / 1000000, nil
}
func usAtFrame(f int64) int64 { return f/44100*1000000 + (f%44100*1000000+22050)/44100 }
func validateInput(input Input) (Input, int64, error) {
	if input.Channels != 1 && input.Channels != 2 {
		return Input{}, 0, errors.New("analysis requires one or two channels")
	}
	// Bound the owned snapshot and report mapping independently of PCM duration.
	if len(input.Sources) > 4096 || len(input.Spans) > 16384 || len(input.Spans) == 0 {
		return Input{}, 0, errors.New("analysis snapshot exceeds metadata budget or has no spans")
	}
	in := Input{Sources: append([]Source(nil), input.Sources...), Spans: append([]Span(nil), input.Spans...), Channels: input.Channels}
	sources := map[string]Source{}
	for _, s := range in.Sources {
		if s.ID == "" || s.Path == "" || s.DurationUS <= 0 || s.SizeBytes < 0 {
			return Input{}, 0, errors.New("invalid source")
		}
		if _, ok := sources[s.ID]; ok {
			return Input{}, 0, errors.New("duplicate source ID")
		}
		sources[s.ID] = s
	}
	var duration int64
	referenced := map[string]bool{}
	for _, span := range in.Spans {
		var length int64
		if span.SourceID == "" {
			if span.StartUS != 0 || span.EndUS != 0 || span.SilenceUS <= 0 {
				return Input{}, 0, errors.New("invalid silence span")
			}
			length = span.SilenceUS
			a, e := frameAtUS(length)
			if e != nil || a == 0 {
				return Input{}, 0, errors.New("invalid silence frame range")
			}
		} else {
			s, ok := sources[span.SourceID]
			if !ok || span.SilenceUS != 0 || span.StartUS < 0 || span.EndUS <= span.StartUS || span.EndUS > s.DurationUS {
				return Input{}, 0, errors.New("invalid source span")
			}
			a, e := frameAtUS(span.StartUS)
			if e != nil {
				return Input{}, 0, e
			}
			b, e := frameAtUS(span.EndUS)
			if e != nil || b <= a {
				return Input{}, 0, errors.New("invalid source frame range")
			}
			length = span.EndUS - span.StartUS
			referenced[span.SourceID] = true
		}
		if duration > math.MaxInt64-length {
			return Input{}, 0, errors.New("timeline duration overflow")
		}
		duration += length
	}
	in.Sources = in.Sources[:0]
	for _, s := range input.Sources {
		if referenced[s.ID] {
			in.Sources = append(in.Sources, s)
		}
	}
	return in, duration, nil
}

type mappedSpan struct {
	span                    Span
	start, end, sourceStart int64
}
type collector struct {
	report          *Report
	spans           []mappedSpan
	provenanceCount int
	limited         map[string]bool
	mappingLimited  bool
}

func priority(kind string) int {
	switch kind {
	case "decode_error":
		return 0
	case "timestamp_discontinuity", "missing_timestamp":
		return 1
	case "repetition":
		return 2
	case "clipping":
		return 3
	default:
		return 4
	}
}
func (c *collector) limitation(s string) {
	for _, v := range c.report.Limitations {
		if v == s {
			return
		}
	}
	c.report.Limitations = append(c.report.Limitations, s)
}
func boundedDetail(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) > MaxDetailBytes {
		s = s[:MaxDetailBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
func (c *collector) add(f Finding) string {
	c.report.Counts[f.Kind]++
	if c.mappingLimited {
		c.limited[f.Kind] = true
	}
	f.Detail = boundedDetail(f.Detail)
	f.ID = fmt.Sprintf("finding-%d", totalCounts(c.report.Counts))
	if f.Provenance == nil {
		f.Provenance = []Provenance{}
	}
	if len(c.report.Findings) < MaxFindings {
		c.report.Findings = append(c.report.Findings, f)
		return f.ID
	}
	c.limited[f.Kind] = true
	c.limitation("finding_limit: only 200 findings are displayed; counts include all detected findings")
	worst := 0
	for i := range c.report.Findings {
		if priority(c.report.Findings[i].Kind) > priority(c.report.Findings[worst].Kind) {
			worst = i
		}
	}
	if priority(f.Kind) < priority(c.report.Findings[worst].Kind) {
		c.limited[c.report.Findings[worst].Kind] = true
		c.report.Findings[worst] = f
		return f.ID
	}
	return ""
}
func totalCounts(counts map[string]int64) int64 {
	var n int64
	for _, v := range counts {
		n += v
	}
	return n
}
func (c *collector) provenance(start, end int64) []Provenance {
	var out []Provenance
	first := sort.Search(len(c.spans), func(i int) bool { return c.spans[i].end > start })
	for _, m := range c.spans[first:] {
		if m.start >= end {
			break
		}
		a, b := max(start, m.start), min(end, m.end)
		if b <= a {
			continue
		}
		if c.provenanceCount >= 8192 {
			c.mappingLimited = true
			c.limitation("provenance_limit: additional source mappings omitted")
			break
		}
		p := Provenance{SourceID: m.span.SourceID, OutputStartUS: usAtFrame(a), OutputEndUS: usAtFrame(b), Synthetic: m.span.SourceID == ""}
		if !p.Synthetic {
			p.SourceStartUS = usAtFrame(m.sourceStart + a - m.start)
			p.SourceEndUS = usAtFrame(m.sourceStart + b - m.start)
			if a == m.start {
				p.SourceStartUS = m.span.StartUS
			}
			if b == m.end {
				p.SourceEndUS = m.span.EndUS
			}
		}
		out = append(out, p)
		c.provenanceCount++
	}
	return out
}
func (c *collector) signal(r signalRun) {
	a, b := usAtFrame(r.start), usAtFrame(r.end)
	detail := "Long silence or very quiet passage: every channel is at or below -50 dBFS for at least 5 seconds."
	if r.kind == "clipping" {
		detail = "Possible clipping: at least 3 consecutive frames in one channel reach 0.999 on the 44.1 kHz analysis grid; legitimate or resampled peaks can qualify."
	}
	c.add(Finding{Kind: r.kind, Severity: "info", Detail: detail, StartUS: &a, EndUS: &b, Provenance: c.provenance(r.start, r.end)})
}

func Analyze(ctx context.Context, input Input, opts Options, progress func(Progress)) (Report, error) {
	return analyze(ctx, input, opts, progress, defaultDependencies())
}
func analyze(ctx context.Context, input Input, opts Options, progress func(Progress), deps dependencies) (result Report, resultErr error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	in, duration, err := validateInput(input)
	if err != nil {
		return Report{}, err
	}
	report := Report{ID: uuid.NewString(), DetectorVersion: DetectorVersion, DurationUS: duration, Sources: []Fingerprint{}, Findings: []Finding{}, Counts: map[string]int64{}, Coverage: []Coverage{}, Limitations: []string{}}
	emit := func(p Progress) {
		if progress != nil {
			progress(p)
		}
	}
	for i, s := range in.Sources {
		emit(Progress{"hash-before", int64(i), int64(len(in.Sources))})
		fp, e := sourceHash(ctx, s, deps.open)
		if e != nil {
			return Report{}, e
		}
		report.Sources = append(report.Sources, fp)
	}
	emit(Progress{"hash-before", int64(len(in.Sources)), int64(len(in.Sources))})
	sources := map[string]Source{}
	for _, s := range in.Sources {
		sources[s.ID] = s
	}
	c := collector{report: &report, limited: map[string]bool{}}
	var plannedFrames int64
	for _, s := range in.Spans {
		start, _ := frameAtUS(s.StartUS)
		end, _ := frameAtUS(s.EndUS)
		n := end - start
		if s.SourceID == "" {
			n, _ = frameAtUS(s.SilenceUS)
		}
		if plannedFrames > math.MaxInt64-n {
			return Report{}, errors.New("timeline frame overflow")
		}
		c.spans = append(c.spans, mappedSpan{s, plannedFrames, plannedFrames + n, start})
		plannedFrames += n
	}
	repeats, err := newRepeatScanner(ctx, opts.CacheDir, in.Channels, deps.repeatLimits)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if e := repeats.Close(); e != nil {
			resultErr = errors.Join(resultErr, e)
			result = Report{}
		}
	}()
	scanner := newSignalScanner(in.Channels, c.signal)
	complete := true
	var sourceFrames int64
	// Keys survive display omission/replacement. An empty value records a source
	// event whose finding was not retained; it must still deduplicate counts.
	type observationKey struct {
		sourceID, kind string
		frame          int64
		located        bool
	}
	observations := map[observationKey]string{}
	observationBudget := max(0, min(deps.maxObservations, maxObservationKeys))
	for _, m := range c.spans {
		if err = ctx.Err(); err != nil {
			return Report{}, err
		}
		if scanner.position >= deps.maxFrames {
			complete = false
			c.limitation("duration_limit: maximum analyzed duration is 24 hours")
			break
		}
		n := min(m.end-m.start, deps.maxFrames-scanner.position)
		if m.span.SourceID == "" {
			scanner.flush()
			scanner.position += n
			if e := repeats.Break(scanner.position); e != nil {
				return Report{}, e
			}
		} else {
			var emitted int64
			inspection, e := deps.inspect(ctx, sources[m.span.SourceID].Path, in.Channels, m.sourceStart, m.sourceStart+n, func(b []float32) error {
				if e := ctx.Err(); e != nil {
					return e
				}
				if len(b)%in.Channels != 0 || len(b)/in.Channels > 16384 || emitted+int64(len(b)/in.Channels) > n {
					return errors.New("invalid inspection PCM chunk")
				}
				for _, v := range b {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						return errors.New("nonfinite decoded sample")
					}
				}
				if e := repeats.Push(b, scanner.position); e != nil {
					return e
				}
				scanner.consume(b)
				emitted += int64(len(b) / in.Channels)
				sourceFrames += int64(len(b) / in.Channels)
				emit(Progress{"decode", usAtFrame(scanner.position), duration})
				return nil
			}, func(p audio.PCMProblem) {
				f := Finding{Kind: p.Kind, Severity: "warning", Detail: p.Detail, SourceID: m.span.SourceID, OriginalTimestampUS: p.OriginalTimestampUS}
				if p.Kind == "missing_timestamp" {
					f.Severity = "info"
				}
				key := observationKey{sourceID: m.span.SourceID, kind: p.Kind}
				if p.SourceFrame != nil && *p.SourceFrame >= m.sourceStart && *p.SourceFrame <= m.sourceStart+n {
					sourceUS := usAtFrame(*p.SourceFrame)
					f.SourceStartUS = &sourceUS
					f.SourceEndUS = &sourceUS
					a := usAtFrame(m.start + *p.SourceFrame - m.sourceStart)
					f.StartUS = &a
					f.EndUS = &a
					key.frame = *p.SourceFrame
					key.located = true
					f.Provenance = []Provenance{{SourceID: m.span.SourceID, SourceStartUS: sourceUS, SourceEndUS: sourceUS, OutputStartUS: a, OutputEndUS: a}}
				}
				if id, ok := observations[key]; ok {
					for index := range report.Findings {
						if report.Findings[index].ID == id {
							if len(report.Findings[index].Provenance) < 128 {
								report.Findings[index].Provenance = append(report.Findings[index].Provenance, f.Provenance...)
							} else {
								c.limited[p.Kind] = true
								c.limitation("observation_occurrence_limit: additional output occurrences omitted")
							}
							break
						}
					}
					return
				}
				id := c.add(f)
				if len(observations) < observationBudget {
					observations[key] = id
				} else {
					c.limited[p.Kind] = true
					c.limitation("observation_dedup_limit: " + p.Kind + " count is an upper bound on distinct source events; events beyond the bounded deduplication set are counted per output occurrence")
				}

			})
			if e != nil {
				return Report{}, e
			}
			if inspection.EmittedFrames != emitted {
				return Report{}, errors.New("inspection frame count mismatch")
			}
			if !inspection.Complete {
				complete = false
				scanner.flush()
				if e := repeats.Break(scanner.position); e != nil {
					return Report{}, e
				}
				c.limitation("decode_incomplete: media decoding stopped before the requested range completed")
				break
			}
		}
		if n < m.end-m.start {
			complete = false
			c.limitation("duration_limit: maximum analyzed duration is 24 hours")
			break
		}
	}
	scanner.flush()
	report.ScannedUS = usAtFrame(scanner.position)
	if complete {
		report.ScannedUS = duration
	}
	emit(Progress{"repeat-verify", 0, usAtFrame(sourceFrames)})
	repeatFindings, repeatCoverage, repeatWarnings, err := repeats.Finish()
	if err != nil {
		return Report{}, err
	}
	for _, warning := range repeatWarnings {
		c.limitation(warning)
	}
	for _, finding := range repeatFindings {
		a, _ := frameAtUS(*finding.StartUS)
		b, _ := frameAtUS(*finding.EndUS)
		secondA, _ := frameAtUS(*finding.SecondStartUS)
		secondB, _ := frameAtUS(*finding.SecondEndUS)
		finding.Provenance = append(c.provenance(a, b), c.provenance(secondA, secondB)...)
		c.add(finding)
	}
	repeatCoverage.Complete = repeatCoverage.Complete && complete && !c.limited["repetition"]
	report.RepeatScannedUS = repeatCoverage.ScannedUS
	emit(Progress{"repeat-verify", report.RepeatScannedUS, usAtFrame(sourceFrames)})
	if err = checkSources(ctx, in, report.Sources, deps.open, progress); err != nil {
		return Report{}, err
	}
	for _, kind := range []string{"decode_error", "timestamp_discontinuity", "missing_timestamp", "clipping", "quiet"} {
		scanned := report.ScannedUS
		if kind == "clipping" || kind == "quiet" {
			scanned = usAtFrame(sourceFrames)
		}
		report.Coverage = append(report.Coverage, Coverage{kind, complete && !c.limited[kind], scanned})
	}
	report.Coverage = append(report.Coverage, repeatCoverage)
	sort.SliceStable(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if priority(a.Kind) != priority(b.Kind) {
			return priority(a.Kind) < priority(b.Kind)
		}
		if a.StartUS == nil {
			return b.StartUS != nil
		}
		if b.StartUS == nil {
			return false
		}
		return *a.StartUS < *b.StartUS
	})
	for i := range report.Findings {
		report.Findings[i].ID = fmt.Sprintf("finding-%d", i+1)
	}
	return report, nil
}
