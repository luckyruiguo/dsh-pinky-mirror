//go:build !darwin && !windows

package ios

import (
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// The paths below iOS 27 encode their picture with macOS's VideoToolbox on
// macOS and with FFmpeg's Media Foundation encoder on Windows; other systems
// have no encoder Pinky can call without cgo yet.

type h264Encoder struct{ w, h int }

// encoderName is the encoder's name for the log; this system has none.
func (e *h264Encoder) encoderName() string { return "none" }

type encodedFrame struct {
	data     []byte
	config   []byte
	keyFrame bool
}

func newH264Encoder(w, h, fps, bitRate int) (*h264Encoder, error) {
	return nil, h264Available()
}

func (e *h264Encoder) encode(y, uv []byte, pts time.Duration, forceKey bool) ([]encodedFrame, error) {
	return nil, h264Available()
}

func (e *h264Encoder) Close() {}

func h264Available() error {
	return fmt.Errorf("the live picture of iPhones below iOS 27 needs an H.264 encoder Pinky can call: "+
		"VideoToolbox on macOS, Media Foundation on Windows, and none on this system: %w", driver.ErrUnsupported)
}
