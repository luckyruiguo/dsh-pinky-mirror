//go:build !e2e

package update

// notarizationRequired: a downloaded Pinky.app is swapped in only when Apple
// notarized it (install_darwin.go). The e2e build turns it off, as the
// end-to-end test signs its bundles but cannot notarize them; release
// builds never carry the e2e tag.
const notarizationRequired = true
