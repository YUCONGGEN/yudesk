package core

import (
	"bytes"
	"errors"
	"image/jpeg"
)

// Frame is always a complete JPEG image, never an independently droppable dirty
// tile. This preview negotiates TileDelta=false with desktop peers.
type Frame struct {
	Data     []byte
	Width    int
	Height   int
	Revision int64
}

const maxFrameBytes = 8 << 20
const maxFramePixels = 3840 * 2160

func validateJPEG(data []byte, width, height int) error {
	if len(data) == 0 || len(data) > maxFrameBytes || width < 1 || height < 1 || width > 8192 || height > 8192 || int64(width)*int64(height) > maxFramePixels {
		return errors.New("画面尺寸或数据超过安全上限")
	}
	c, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return errors.New("无效 JPEG 画面")
	}
	if c.Width != width || c.Height != height {
		return errors.New("画面尺寸与数据不一致")
	}
	return nil
}

// SubmitJPEG copies one latest frame. Native capture keeps at most one acquired
// image; blocked networking cannot retain old captures or increase memory.
func (e *Engine) SubmitJPEG(data []byte, width, height int) error {
	if err := validateJPEG(data, width, height); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.permittedLocked() || !e.state.Sharing || !e.streaming {
		return nil
	}
	e.frameSequence++
	e.frame = &Frame{Data: append([]byte(nil), data...), Width: width, Height: height, Revision: e.frameSequence}
	select {
	case e.frameWake <- struct{}{}:
	default:
	}
	return nil
}
