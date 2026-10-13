package audio

import (
	"github.com/asticode/go-astiav"
	"testing"
)

func TestBundledAudioCapabilities(t *testing.T) {
	for _, n := range []string{"mp3", "mp3float", "aac", "aac_fixed", "vorbis", "opus", "flac", "pcm_s16le", "pcm_s24le", "pcm_s32le", "pcm_f32le", "mjpeg", "png"} {
		if astiav.FindDecoderByName(n) == nil {
			t.Errorf("missing decoder %s", n)
		}
	}
	for _, n := range []string{"aac", "flac", "pcm_f32le", "pcm_s24le"} {
		if astiav.FindEncoderByName(n) == nil {
			t.Errorf("missing encoder %s", n)
		}
	}
	for _, n := range []string{"wav", "flac", "ipod"} {
		f, e := astiav.AllocOutputFormatContext(nil, n, "")
		if e != nil || f == nil {
			t.Errorf("missing muxer %s: %v", n, e)
		} else {
			f.Free()
		}
	}
	for _, n := range []string{"ebur128", "volume"} {
		if astiav.FindFilterByName(n) == nil {
			t.Errorf("missing filter %s", n)
		}
	}
}
