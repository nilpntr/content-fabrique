package quicklook

import (
	"encoding/base64"
	"fmt"
)

// Two tiny JPEGs, written out so that the bytes (and with them the cassette key of a request that carries them) never depend
// on the JPEG encoder of the Go version running the tests. sharpB64 is a 32x32 checkerboard, flatB64 a flat grey square.
const sharpB64 = "/9j/2wCEAAMCAgMCAgMDAwMEAwMEBQgFBQQEBQoHBwYIDAoMDAsKCwsNDhIQDQ4RDgsLEBYQERMUFRUVDA8XGBYUGBIUFRQBAwQE" +
	"BQQFCQUFCRQNCw0UFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFP/AAAsIACAAIAEBEQD/" +
	"xADSAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNC" +
	"scEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SV" +
	"lpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+v/aAAgBAQAAPwBP+RC/" +
	"6pZ/wgP/AHGP+FVfbvz/ALa/tTd/tfZPM/h20f8AIhf9Us/4QH/uMf8ACqvt35/21/am7/a+yeZ/Dto/5EL/AKpZ/wAID/3GP+FV" +
	"fbvz/tr+1N3+19k8z+HbR/yIX/VLP+EB/wC4x/wqr7d+f9tf2pu/2vsnmfw7aP8AkQv+qWf8ID/3GP8AhVX278/7a/tTd/tfZPM/" +
	"h20f8iF/1Sz/AIQH/uMf8Kq+3fn/AG1/am7/AGvsnmfw7aP+RC/6pZ/wgP8A3GP+FVfbvz/tr+1N3+19k8z+HbR/yIX/AFSz/hAf" +
	"+4x/wqr7d+f9tf2pu/2vsnmfw7aP+RC/6pZ/wgP/AHGP+FVfbvz/ALa/tTd/tfZPM/h20f8AIhf9Us/4QH/uMf8ACqvt35/21/am" +
	"7/a+yeZ/Dto/5EL/AKpZ/wAID/3GP+FVfbvz/tr+1N3+19k8z+HbR/yIX/VLP+EB/wC4x/wqr7d+f9tf2pu/2vsnmfw7aP8AkQv+" +
	"qWf8ID/3GP8AhVX278/7a/tTd/tfZPM/h20f8iF/1Sz/AIQH/uMf8Kq+3fn/AG1/am7/AGvsnmfw7aP+RC/6pZ/wgP8A3GP+FVfb" +
	"vz/tr+1N3+19k8z+HbR/yIX/AFSz/hAf+4x/wqr7d+f9tf2pu/2vsnmfw7a//9k="

const flatB64 = "/9j/2wCEAAMCAgMCAgMDAwMEAwMEBQgFBQQEBQoHBwYIDAoMDAsKCwsNDhIQDQ4RDgsLEBYQERMUFRUVDA8XGBYUGBIUFRQBAwQE" +
	"BQQFCQUFCRQNCw0UFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFP/AAAsIACAAIAEBEQD/" +
	"xADSAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNC" +
	"scEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SV" +
	"lpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+v/aAAgBAQAAPwAooooo" +
	"oooooooooor/2Q=="

func mustB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// fixtureFrame is the JPEG a fake sandbox writes for candidate i: the sharp one for i%3 == 1, else the flat one, with a
// JPEG comment segment holding i so that every candidate has distinct bytes (and the decoder skips it).
func fixtureFrame(i int) []byte {
	base := mustB64(flatB64)
	if i%3 == 1 {
		base = mustB64(sharpB64)
	}
	com := []byte(fmt.Sprintf("candidate %02d", i))
	seg := append([]byte{0xFF, 0xFE, byte((len(com) + 2) >> 8), byte((len(com) + 2) & 0xFF)}, com...)
	out := append([]byte{}, base[:2]...) // SOI
	out = append(out, seg...)
	return append(out, base[2:]...)
}
