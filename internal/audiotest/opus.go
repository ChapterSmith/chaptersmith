package audiotest

import (
	"encoding/binary"
	"os"
	"testing"
)

// WriteOpusSilence uses the RFC 6716 DTX silence packet (F8 FF FE), in an
// RFC 7845 Ogg stream: six 20ms packets, 312 preskip and 648 discarded samples.
// The audible duration is exactly 4800/48000 seconds. No Opus encoder is needed.
func WriteOpusSilence(t testing.TB, path string) {
	t.Helper()
	var out []byte
	page := func(seq uint32, flags byte, granule uint64, packet []byte) {
		b := make([]byte, 28+len(packet))
		copy(b, "OggS")
		b[5] = flags
		binary.LittleEndian.PutUint64(b[6:], granule)
		binary.LittleEndian.PutUint32(b[14:], 1)
		binary.LittleEndian.PutUint32(b[18:], seq)
		b[26] = 1
		b[27] = byte(len(packet))
		copy(b[28:], packet)
		var crc uint32
		for _, v := range b {
			crc ^= uint32(v) << 24
			for range 8 {
				if crc&0x80000000 != 0 {
					crc = crc<<1 ^ 0x04c11db7
				} else {
					crc <<= 1
				}
			}
		}
		binary.LittleEndian.PutUint32(b[22:], crc)
		out = append(out, b...)
	}
	head := make([]byte, 19)
	copy(head, "OpusHead")
	head[8] = 1
	head[9] = 1
	binary.LittleEndian.PutUint16(head[10:], 312)
	binary.LittleEndian.PutUint32(head[12:], 48000)
	page(0, 2, 0, head)
	tags := make([]byte, 16)
	copy(tags, "OpusTags")
	page(1, 0, 0, tags)
	for i := 0; i < 6; i++ {
		g := uint64((i + 1) * 960)
		var flags byte
		if i == 5 {
			g = 5112
			flags = 4
		}
		page(uint32(i+2), flags, g, []byte{0xf8, 0xff, 0xfe})
	}
	if e := os.WriteFile(path, out, 0600); e != nil {
		t.Fatal(e)
	}
}
