// Package analysis diagnoses an immutable edited audio timeline. It has no
// dependency on book, GUI or rendering services.
package analysis

import "os"

const DetectorVersion = "repair-analysis-v1"
const (
	MaxDurationUS       int64 = 24 * 60 * 60 * 1000000
	MaxPCMBytes         int64 = 4 << 30
	MaxIndexBytes       int64 = 128 << 20
	MaxSQLiteCacheBytes int64 = 8 << 20
	MaxGoBufferBytes    int64 = 32 << 20
	MaxPairs                  = 1000
	MaxOccurrences            = 8
	MaxFindings               = 200
	MaxDetailBytes            = 512
)

type Source struct {
	ID         string `json:"id"`
	Path       string `json:"-"`
	DurationUS int64  `json:"durationUs"`
	SizeBytes  int64  `json:"sizeBytes,string"`
	MTimeNS    int64  `json:"mtimeNs,string"`
}
type Span struct {
	SourceID  string `json:"sourceId"`
	StartUS   int64  `json:"startUs"`
	EndUS     int64  `json:"endUs"`
	SilenceUS int64  `json:"silenceUs"`
}
type Input struct {
	Sources  []Source `json:"sources"`
	Spans    []Span   `json:"spans"`
	Channels int      `json:"channels"`
}
type Options struct{ CacheDir string }
type Fingerprint struct {
	SourceID  string `json:"sourceId"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes,string"`
	MTimeNS   int64  `json:"mtimeNs,string"`
	identity  os.FileInfo
}
type Provenance struct {
	SourceID      string `json:"sourceId"`
	SourceStartUS int64  `json:"sourceStartUs"`
	SourceEndUS   int64  `json:"sourceEndUs"`
	OutputStartUS int64  `json:"outputStartUs"`
	OutputEndUS   int64  `json:"outputEndUs"`
	Synthetic     bool   `json:"synthetic"`
}
type Finding struct {
	ID                  string       `json:"id"`
	Kind                string       `json:"kind"`
	Severity            string       `json:"severity"`
	Detail              string       `json:"detail"`
	Verification        string       `json:"verification"`
	StartUS             *int64       `json:"startUs"`
	EndUS               *int64       `json:"endUs"`
	SecondStartUS       *int64       `json:"secondStartUs"`
	SecondEndUS         *int64       `json:"secondEndUs"`
	OriginalTimestampUS *int64       `json:"originalTimestampUs"`
	SourceID            string       `json:"sourceId"`
	SourceStartUS       *int64       `json:"sourceStartUs"`
	SourceEndUS         *int64       `json:"sourceEndUs"`
	Provenance          []Provenance `json:"provenance"`
}
type Coverage struct {
	Detector  string `json:"detector"`
	Complete  bool   `json:"complete"`
	ScannedUS int64  `json:"scannedUs"`
}
type Report struct {
	ID              string           `json:"id"`
	DetectorVersion string           `json:"detectorVersion"`
	Sources         []Fingerprint    `json:"sources"`
	DurationUS      int64            `json:"durationUs"`
	ScannedUS       int64            `json:"scannedUs"`
	RepeatScannedUS int64            `json:"repeatScannedUs"`
	Coverage        []Coverage       `json:"coverage"`
	Findings        []Finding        `json:"findings"`
	Counts          map[string]int64 `json:"counts"`
	Limitations     []string         `json:"limitations"`
}
type Progress struct {
	Phase   string `json:"phase"`
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
}
