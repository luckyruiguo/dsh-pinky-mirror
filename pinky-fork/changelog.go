// Package pinky holds files from the repository's root that the app
// embeds.
package pinky

import _ "embed"

// Changelog is CHANGELOG.md: newest first, a "## <version> (<YYYY-MM-DD>)"
// section of bullets per release. What's new shows it after an update.
//
//go:embed CHANGELOG.md
var Changelog string

// ChangelogTranslations are CHANGELOG.md's translations, by the language
// tags of the app's catalogs (cmd/pinky/locales): the same headings, and
// under each the same notes in that language. What's new shows the one of
// the app's language, and CHANGELOG.md's notes of a version it lacks.
var ChangelogTranslations = map[string]string{
	"zh-Hans": changelogZhHans,
}

//go:embed CHANGELOG.zh-CN.md
var changelogZhHans string
