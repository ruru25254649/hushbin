package imageproxy

import (
	"bytes"
	"encoding/binary"
)

// MinImageBytes is the smallest body treated as a possible image.
const MinImageBytes = 8

var pngSig = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// Sniff returns the media type of an image by its magic bytes, or "" if it
// is not one of PNG, JPEG, GIF, WebP, BMP or AVIF. Upstream Content-Type
// headers are never trusted; SVG (scriptable) and everything else are
// rejected.
func Sniff(b []byte) string {
	if len(b) < MinImageBytes {
		return ""
	}
	switch {
	case bytes.HasPrefix(b, pngSig):
		return "image/png"
	case b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	case isBMP(b):
		return "image/bmp"
	case isAVIF(b):
		return "image/avif"
	}
	return ""
}

// isBMP checks "BM" plus a known DIB header size, since two bytes alone
// would also match plain text.
func isBMP(b []byte) bool {
	if len(b) < 26 || b[0] != 'B' || b[1] != 'M' {
		return false
	}
	switch binary.LittleEndian.Uint32(b[14:18]) {
	case 12, 40, 52, 56, 64, 108, 124:
		return true
	}
	return false
}

// isAVIF checks an ISO-BMFF "ftyp" box whose major brand is avif/avis, or a
// generic HEIF major brand (mif1/msf1) listing avif/avis as compatible.
func isAVIF(b []byte) bool {
	if len(b) < 16 || !bytes.Equal(b[4:8], []byte("ftyp")) {
		return false
	}
	isAV := func(brand []byte) bool {
		return bytes.Equal(brand, []byte("avif")) || bytes.Equal(brand, []byte("avis"))
	}
	major := b[8:12]
	if isAV(major) {
		return true
	}
	if !bytes.Equal(major, []byte("mif1")) && !bytes.Equal(major, []byte("msf1")) {
		return false
	}
	size := int(binary.BigEndian.Uint32(b[0:4]))
	if size < 16 || size > 4096 {
		return false
	}
	if size > len(b) {
		size = len(b)
	}
	for off := 16; off+4 <= size; off += 4 {
		if isAV(b[off : off+4]) {
			return true
		}
	}
	return false
}
