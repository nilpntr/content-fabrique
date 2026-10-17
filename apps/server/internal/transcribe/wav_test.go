package transcribe

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWAVRoundTrip(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	got, err := ParseWAV(EncodeWAV(in))
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("%v %v", got, err)
	}
	if DurationMsOf(SampleRate*3/2) != 1500 {
		t.Error("duration")
	}
}

func TestParseWAVSkipsOtherChunksAndToleratesAStreamedSize(t *testing.T) {
	b := EncodeWAV([]int16{7, 8, 9})
	// Insert a LIST chunk between fmt and data, and make the data size the 0xFFFFFFFF ffmpeg leaves on a pipe.
	list := append([]byte("LIST\x05\x00\x00\x00hello"), 0) // odd size: padded to even
	withList := append(append(append([]byte{}, b[:36]...), list...), b[36:]...)
	got, err := ParseWAV(withList)
	if err != nil || !reflect.DeepEqual(got, []int16{7, 8, 9}) {
		t.Fatalf("%v %v", got, err)
	}
	binary.LittleEndian.PutUint32(b[40:], 0xFFFFFFFF)
	got, err = ParseWAV(b)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestParseWAVRefusesOtherFormats(t *testing.T) {
	good := EncodeWAV([]int16{1, 2})
	for name, mutate := range map[string]func([]byte){
		"stereo":      func(b []byte) { binary.LittleEndian.PutUint16(b[22:], 2) },
		"44.1 kHz":    func(b []byte) { binary.LittleEndian.PutUint32(b[24:], 44100) },
		"8 bit":       func(b []byte) { binary.LittleEndian.PutUint16(b[34:], 8) },
		"float":       func(b []byte) { binary.LittleEndian.PutUint16(b[20:], 3) },
		"not RIFF":    func(b []byte) { copy(b, "RIFX") },
		"not WAVE":    func(b []byte) { copy(b[8:], "AVI ") },
		"no data":     func(b []byte) { copy(b[36:], "junk") },
		"short fmt":   func(b []byte) { binary.LittleEndian.PutUint32(b[16:], 4) },
		"huge chunk":  func(b []byte) { binary.LittleEndian.PutUint32(b[16:], 0x7FFFFFF0) },
		"data first":  func(b []byte) { copy(b[12:], "data") },
		"empty bytes": nil,
	} {
		b := append([]byte{}, good...)
		if mutate == nil {
			b = nil
		} else {
			mutate(b)
		}
		if _, err := ParseWAV(b); !errors.Is(err, ErrUnusableAudio) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestReadWAVLimits(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	if err := os.WriteFile(p, EncodeWAV([]int16{1, 2, 3}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadWAV(p); err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ReadWAV(dir); !errors.Is(err, ErrUnusableAudio) {
		t.Errorf("a directory: %v", err)
	}
	if _, err := ReadWAV(filepath.Join(dir, "missing.wav")); err == nil {
		t.Error("missing file")
	}
}
