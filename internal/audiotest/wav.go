// Package audiotest provides small, independently readable PCM fixtures.
package audiotest

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

func WriteWAV(t testing.TB, path string, rate, channels int, samples []int16) {
	t.Helper()
	b := make([]byte, 44+2*len(samples))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(b[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(samples)))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}

// ReadPCM parses short float WAV/RF64 fixtures without using the decoder under test.
func ReadPCM(t testing.TB, path string, channels int) []float32 {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if len(b) < 12 || string(b[8:12]) != "WAVE" {
		t.Fatal("not WAV")
	}
	var dataLen uint64
	for p := 12; p+8 <= len(b); {
		id := string(b[p : p+4])
		n := uint64(binary.LittleEndian.Uint32(b[p+4:]))
		p += 8
		if id == "ds64" {
			dataLen = binary.LittleEndian.Uint64(b[p+8:])
		}
		if id == "data" {
			if n == math.MaxUint32 {
				n = dataLen
			}
			if n > uint64(len(b)-p) || n%uint64(4*channels) != 0 {
				t.Fatal("invalid data length")
			}
			out := make([]float32, n/4)
			for i := range out {
				out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[p+4*i:]))
			}
			return out
		}
		p += int(n + (n & 1))
	}
	t.Fatal("no data")
	return nil
}
