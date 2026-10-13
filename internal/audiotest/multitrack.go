package audiotest

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

// WriteAudioTracks wraps basic 16-bit PCM WAV fixtures in independent Matroska
// tracks with exactly one default. It needs no encoder or optional muxer.
func WriteAudioTracks(t testing.TB, path string, inputs []string, defaultTrack int) {
	t.Helper()
	if defaultTrack < 0 || defaultTrack >= len(inputs) || len(inputs) > 126 {
		t.Fatal("invalid fixture tracks")
	}
	element := func(id []byte, payload []byte) []byte {
		b := append([]byte(nil), id...)
		size := make([]byte, 4)
		binary.BigEndian.PutUint32(size, 0x10000000|uint32(len(payload)))
		return append(append(b, size...), payload...)
	}
	uintElement := func(id []byte, n uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, n)
		return element(id, b)
	}
	floatElement := func(id []byte, n float64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(n))
		return element(id, b)
	}
	var header []byte
	for _, f := range []struct {
		id []byte
		n  uint64
	}{
		{[]byte{0x42, 0x86}, 1}, {[]byte{0x42, 0xf7}, 1}, {[]byte{0x42, 0xf2}, 4},
		{[]byte{0x42, 0xf3}, 8}, {[]byte{0x42, 0x87}, 4}, {[]byte{0x42, 0x85}, 2},
	} {
		header = append(header, uintElement(f.id, f.n)...)
	}
	header = append(header, element([]byte{0x42, 0x82}, []byte("matroska"))...)
	var tracks []byte
	cluster := uintElement([]byte{0xe7}, 0)
	var duration float64
	for i, input := range inputs {
		raw, err := os.ReadFile(input)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) < 44 || string(raw[:4]) != "RIFF" || string(raw[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(raw[16:]) != 16 || binary.LittleEndian.Uint16(raw[20:]) != 1 || binary.LittleEndian.Uint16(raw[34:]) != 16 || string(raw[36:40]) != "data" {
			t.Fatal("fixture requires basic 16-bit PCM WAV")
		}
		rate := binary.LittleEndian.Uint32(raw[24:])
		channels := binary.LittleEndian.Uint16(raw[22:])
		samples := raw[44:]
		if rate == 0 || channels == 0 || len(samples)%(2*int(channels)) != 0 {
			t.Fatal("invalid WAV samples")
		}
		duration = math.Max(duration, float64(len(samples)/(2*int(channels)))*1000/float64(rate))
		track := uintElement([]byte{0xd7}, uint64(i+1))
		track = append(track, uintElement([]byte{0x73, 0xc5}, uint64(i+1))...)
		track = append(track, uintElement([]byte{0x83}, 2)...)
		flag := uint64(0)
		if i == defaultTrack {
			flag = 1
		}
		track = append(track, uintElement([]byte{0x88}, flag)...)
		track = append(track, element([]byte{0x86}, []byte("A_PCM/INT/LIT"))...)
		audio := floatElement([]byte{0xb5}, float64(rate))
		audio = append(audio, uintElement([]byte{0x9f}, uint64(channels))...)
		audio = append(audio, uintElement([]byte{0x62, 0x64}, 16)...)
		track = append(track, element([]byte{0xe1}, audio)...)
		tracks = append(tracks, element([]byte{0xae}, track)...)
		block := append([]byte{byte(0x80 | i + 1), 0, 0, 0x80}, samples...)
		cluster = append(cluster, element([]byte{0xa3}, block)...)
	}
	info := uintElement([]byte{0x2a, 0xd7, 0xb1}, 1000000)
	info = append(info, floatElement([]byte{0x44, 0x89}, duration)...)
	segment := element([]byte{0x15, 0x49, 0xa9, 0x66}, info)
	segment = append(segment, element([]byte{0x16, 0x54, 0xae, 0x6b}, tracks)...)
	segment = append(segment, element([]byte{0x1f, 0x43, 0xb6, 0x75}, cluster)...)
	out := element([]byte{0x1a, 0x45, 0xdf, 0xa3}, header)
	out = append(out, element([]byte{0x18, 0x53, 0x80, 0x67}, segment)...)
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
}
