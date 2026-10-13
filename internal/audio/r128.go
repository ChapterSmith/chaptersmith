package audio

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// Loudness describes a complete continuous programme. HasIntegrated distinguishes
// an actual gated R128 measurement from the native filter's -70 LUFS floor.
// Silent means every decoded sample was zero; its true peak is negative infinity.
type Loudness struct {
	IntegratedLUFS, TruePeakDBTP float64
	HasIntegrated, Silent        bool
}

// MeasureR128 decodes files in order through one meter, with no gap or meter
// reset between them. Inputs must already share the final mono/stereo layout;
// channel conversion and editing precede this measurement. dualMono affects
// only mono integrated loudness, with the standard -3.01029995663978 dB pan law.
func MeasureR128(ctx context.Context, paths []string, dualMono bool) (Loudness, error) {
	if e := ctx.Err(); e != nil {
		return Loudness{}, e
	}
	if len(paths) == 0 {
		return Loudness{}, errors.New("R128 requires audio paths")
	}
	// Probe chooses the same first audio stream as DecodePCM. Never choose a
	// layout from dualMono: final stereo playback must retain both channels.
	first, e := ProbeSource(ctx, paths[0])
	if e != nil {
		return Loudness{}, e
	}
	channels := first.Channels
	if channels != 1 && channels != 2 {
		return Loudness{}, errors.New("R128 requires final mono or stereo PCM")
	}
	// SWR's default 32-tap interpolator needs 33 initial frames. Decide
	// whether the entire programme is tiny only after this bounded prefix
	// or actual EOF; chapter/file boundaries never make that decision.
	var meter *r128Meter
	defer func() {
		if meter != nil {
			meter.close()
		}
	}()
	prefix := make([]float32, 0, 33*channels)
	feed := func(samples []float32) error {
		if meter == nil {
			n := min(len(samples), 33*channels-len(prefix))
			prefix = append(prefix, samples[:n]...)
			samples = samples[n:]
			if len(prefix) < 33*channels {
				return nil
			}
			var err error
			meter, err = newR128Meter(channels, dualMono)
			if err != nil {
				return err
			}
			if err = meter.push(prefix, channels); err != nil {
				return err
			}
			prefix = nil
		}
		if len(samples) == 0 {
			return nil
		}
		return meter.push(samples, channels)
	}
	var frames int64
	var samplePeak float64
	for i, path := range paths {
		if i > 0 {
			info, e := ProbeSource(ctx, path)
			if e != nil {
				return Loudness{}, e
			}
			if info.Channels != channels {
				return Loudness{}, errors.New("R128 input channel layouts differ")
			}
		}
		e = DecodePCM(ctx, path, channels, 0, -1, func(samples []float32) error {
			for len(samples) > 0 {
				if e := ctx.Err(); e != nil {
					return e
				}
				n := min(len(samples), 4410*channels) // bounded 100 ms meter input
				count := int64(n / channels)
				if frames > math.MaxInt64-count {
					return errors.New("R128 duration overflow")
				}
				frames += count
				for _, sample := range samples[:n] {
					v := float64(sample)
					if math.IsNaN(v) || math.IsInf(v, 0) {
						return errors.New("nonfinite R128 PCM sample")
					}
					samplePeak = math.Max(samplePeak, math.Abs(v))
				}
				if e := feed(samples[:n]); e != nil {
					return e
				}
				samples = samples[n:]
			}
			return nil
		})
		if e != nil {
			return Loudness{}, e
		}
	}
	if frames == 0 {
		return Loudness{}, errors.New("empty R128 audio")
	}
	if e := ctx.Err(); e != nil {
		return Loudness{}, e
	}
	if meter == nil {
		meter, e = newShortR128Meter(channels, dualMono, int(frames))
		if e != nil {
			return Loudness{}, e
		}
		if e = meter.push(prefix, channels); e != nil {
			return Loudness{}, e
		}
	}
	// Actual EOF drains both the upstream oversampler and the meter's short frame.
	if e := meter.push(nil, channels); e != nil {
		return Loudness{}, e
	}
	integrated, e := meter.value("integrated")
	if e != nil {
		return Loudness{}, e
	}
	peak, e := meter.value("true_peak")
	if e != nil {
		return Loudness{}, e
	}
	silent := samplePeak == 0
	if math.IsNaN(integrated) || math.IsInf(integrated, 0) || math.IsNaN(peak) || math.IsInf(peak, 1) || (!silent && math.IsInf(peak, -1)) {
		return Loudness{}, errors.New("nonfinite R128 filter result")
	}
	if silent {
		peak = math.Inf(-1)
	} else {
		// Oversampling must never report below an original sample's amplitude.
		peak = math.Max(peak, 20*math.Log10(samplePeak))
	}
	// n8.0 initializes I to ABS_THRES (-70) and updates it only when a
	// 400 ms block passes the absolute gate and there are relative-gated blocks.
	// Energy alone or a finite native floor is not an integrated target.
	defined := !silent && frames >= 17640 && integrated > -70
	if e := ctx.Err(); e != nil {
		return Loudness{}, fmt.Errorf("measuring R128: %w", e)
	}
	return Loudness{IntegratedLUFS: integrated, TruePeakDBTP: peak, HasIntegrated: defined, Silent: silent}, nil
}
