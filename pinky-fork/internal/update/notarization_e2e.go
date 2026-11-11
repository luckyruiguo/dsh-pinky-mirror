//go:build e2e

package update

// notarizationRequired is off in the e2e build: the end-to-end test of
// updates installs bundles it signs with the Developer ID but cannot
// notarize (cmd/pinky/update_e2e_test.go).
const notarizationRequired = false
