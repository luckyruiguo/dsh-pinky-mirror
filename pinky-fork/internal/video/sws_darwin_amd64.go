package video

// On an Intel Mac, libswscale's SWS_FAST_BILINEAR writes x86 code at run
// time (MMXEXT's horizontal scaler) and runs it from memory it mapped
// itself. The hardened runtime of a notarized app forbids running unsigned
// pages, so macOS kills Pinky (SIGKILL, Code Signature Invalid) the first
// time a mirror scales that way. Bilinear needs no such code.
const fastBilinearOK = false
