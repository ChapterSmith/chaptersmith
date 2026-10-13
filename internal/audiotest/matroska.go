package audiotest

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

// WrapAACMatroska wraps ADTS packet payloads in minimal Matroska BlockGroups
// with millisecond clocks and 23ms block durations. The coarse container clock
// must not cause a decoder to shorten every 1024-sample AAC frame to 1014 samples.
func WrapAACMatroska(t testing.TB, rawPath, path string) {
	t.Helper()
	raw, e := os.ReadFile(rawPath)
	if e != nil {
		t.Fatal(e)
	}
	element := func(id []byte, payload []byte) []byte {
		b := append([]byte(nil), id...)
		size := make([]byte, 4)
		binary.BigEndian.PutUint32(size, 0x10000000|uint32(len(payload)))
		b = append(b, size...)
		return append(b, payload...)
	}
	uintElement := func(id []byte, n uint64) []byte {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, n)
		return element(id, b)
	}
	var header []byte
	for _, f := range []struct {
		id []byte
		n  uint64
	}{{[]byte{0x42, 0x86}, 1}, {[]byte{0x42, 0xf7}, 1}, {[]byte{0x42, 0xf2}, 4}, {[]byte{0x42, 0xf3}, 8}, {[]byte{0x42, 0x87}, 4}, {[]byte{0x42, 0x85}, 2}} {
		header = append(header, uintElement(f.id, f.n)...)
	}
	header = append(header, element([]byte{0x42, 0x82}, []byte("matroska"))...)
	info := uintElement([]byte{0x2a, 0xd7, 0xb1}, 1000000)
	info = append(info, element([]byte{0x4d, 0x80}, []byte("fixture"))...)
	info = append(info, element([]byte{0x57, 0x41}, []byte("fixture"))...)
	track := uintElement([]byte{0xd7}, 1)
	track = append(track, uintElement([]byte{0x73, 0xc5}, 1)...)
	track = append(track, uintElement([]byte{0x83}, 2)...)
	track = append(track, element([]byte{0x86}, []byte("A_AAC"))...)
	track = append(track, element([]byte{0x63, 0xa2}, []byte{0x12, 0x08})...)
	frequency := make([]byte, 8)
	binary.BigEndian.PutUint64(frequency, math.Float64bits(44100))
	audio := element([]byte{0xb5}, frequency)
	audio = append(audio, uintElement([]byte{0x9f}, 1)...)
	track = append(track, element([]byte{0xe1}, audio)...)
	cluster := uintElement([]byte{0xe7}, 0)
	for i := 0; len(raw) > 0; i++ {
		if len(raw) < 7 || raw[0] != 0xff {
			t.Fatal("invalid ADTS fixture")
		}
		n := int(raw[3]&3)<<11 | int(raw[4])<<3 | int(raw[5]>>5)
		if n < 7 || n > len(raw) {
			t.Fatal("invalid ADTS length")
		}
		block := []byte{0x81, byte((i * 23) >> 8), byte(i * 23), 0}
		block = append(block, raw[7:n]...)
		group := element([]byte{0xa1}, block)
		group = append(group, uintElement([]byte{0x9b}, 23)...)
		cluster = append(cluster, element([]byte{0xa0}, group)...)
		raw = raw[n:]
	}
	segment := element([]byte{0x15, 0x49, 0xa9, 0x66}, info)
	segment = append(segment, element([]byte{0x16, 0x54, 0xae, 0x6b}, element([]byte{0xae}, track))...)
	segment = append(segment, element([]byte{0x1f, 0x43, 0xb6, 0x75}, cluster)...)
	out := element([]byte{0x1a, 0x45, 0xdf, 0xa3}, header)
	out = append(out, element([]byte{0x18, 0x53, 0x80, 0x67}, segment)...)
	if e = os.WriteFile(path, out, 0600); e != nil {
		t.Fatal(e)
	}
}
