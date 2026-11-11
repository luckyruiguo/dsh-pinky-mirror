package android

import "testing"

// The gateway's stream hub asks for key frames through this method.
func TestStreamRequestsKeyFrames(t *testing.T) {
	var _ interface{ RequestKeyFrame() } = (*stream)(nil)
}
