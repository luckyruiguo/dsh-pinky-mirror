//go:build !(darwin && amd64)

package video

// fastBilinearOK reports whether SWS_FAST_BILINEAR is safe here (see
// sws_darwin_amd64.go).
const fastBilinearOK = true
