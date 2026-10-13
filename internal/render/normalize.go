package render

import (
	"chaptersmith/internal/audio"
	"errors"
	"fmt"
	"math"
)

type gainPlan struct {
	GainDB                     float64
	TargetDefined, PeakLimited bool
}

// normalizationGain chooses one steady gain for the complete edited book.
// An undefined target allows attenuation to the peak ceiling, never amplification.
func normalizationGain(measured audio.Loudness) (gainPlan, error) {
	if math.IsNaN(measured.IntegratedLUFS) || math.IsInf(measured.IntegratedLUFS, 1) || measured.HasIntegrated && math.IsInf(measured.IntegratedLUFS, -1) || math.IsNaN(measured.TruePeakDBTP) || math.IsInf(measured.TruePeakDBTP, 1) {
		return gainPlan{}, errors.New("invalid nonfinite loudness measurement")
	}
	if measured.Silent {
		if measured.HasIntegrated || !math.IsInf(measured.TruePeakDBTP, -1) {
			return gainPlan{}, errors.New("inconsistent silent loudness measurement")
		}
		return gainPlan{}, nil
	}
	if math.IsInf(measured.TruePeakDBTP, -1) {
		return gainPlan{}, errors.New("non-silent audio requires a measurable true peak")
	}
	targetGain := 0.0
	if measured.HasIntegrated {
		targetGain = -18 - measured.IntegratedLUFS
	}
	peakGain := -2 - measured.TruePeakDBTP
	gain := math.Min(targetGain, peakGain)
	if math.IsNaN(gain) || math.IsInf(gain, 0) {
		return gainPlan{}, errors.New("nonfinite normalization gain")
	}
	return gainPlan{GainDB: gain, TargetDefined: measured.HasIntegrated, PeakLimited: peakGain < targetGain}, nil
}

// normalizationReport describes measured values. It never estimates achieved
// output LUFS by adding gain: callers meter final decoded exports separately.
func normalizationReport(measured audio.Loudness, gain gainPlan) string {
	loudness := fmt.Sprintf("%.2f LUFS", measured.IntegratedLUFS)
	if !gain.TargetDefined {
		loudness = "undefined LUFS target (audio shorter than 400 ms or no gated blocks)"
		if measured.Silent {
			loudness = "undefined LUFS target (silent audio)"
		}
	}
	peak := fmt.Sprintf("%.2f dBTP", measured.TruePeakDBTP)
	if measured.Silent {
		peak = "-Inf dBTP (silent audio)"
	}
	limited := ""
	if gain.PeakLimited {
		limited = "; peak-limited by -2 dBTP ceiling"
	}
	return fmt.Sprintf("Measured %s; true peak %s; fixed gain %+.2f dB%s", loudness, peak, gain.GainDB, limited)
}
