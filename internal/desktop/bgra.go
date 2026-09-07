package desktop

import (
	"encoding/binary"
	"image"
)

// Copy transient Windows DIB pixels directly into the owned encoder buffer.
// Source memory is never retained or modified; no in-place swizzle + copy pass.
func ownedBGRA(source []byte, width, height int, buffer *image.RGBA) *image.RGBA {
	result := reusableCaptureBuffer(buffer, width, height)
	dst := result.Pix[:width*height*4]
	i := 0
	for ; i+8 <= len(dst); i += 8 {
		v := binary.LittleEndian.Uint64(source[i : i+8])
		v = (v&0x000000ff000000ff)<<16 | (v&0x00ff000000ff0000)>>16 | v&0x0000ff000000ff00 | 0xff000000ff000000
		binary.LittleEndian.PutUint64(dst[i:i+8], v)
	}
	if i < len(dst) {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = source[i+2], source[i+1], source[i], 255
	}
	return result
}
