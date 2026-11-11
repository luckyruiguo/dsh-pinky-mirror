package ios

import "testing"

// The gateway's stream hub asks for key frames through this method.
func TestMediaStreamRequestsKeyFrames(t *testing.T) {
	var _ interface{ RequestKeyFrame() } = (*mediaStream)(nil)
}
