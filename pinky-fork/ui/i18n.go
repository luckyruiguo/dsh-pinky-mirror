package ui

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
)

// The client's words are in the catalogs of cmd/pinky/locales, one
// function per message (see docs/notes/client.md, "i18n"). These helpers
// name the policy's rules and join lists in the language the app shows;
// gwtext.go says the gateway's English steps, approvals and status lines
// in it.

// ruleNames are the rules of the policy presets and the built-in ones, as
// approvals and steps name them.
var ruleNames = map[string]func() string{
	"Payments":           locales.RulePayments,
	"Messages":           locales.RuleMessages,
	"Commits":            locales.RuleCommits,
	"Installs and links": locales.RuleInstallsLinks,
	"Calls":              locales.RuleCalls,
	"Settings changes":   locales.RuleSettingsChanges,
	"Phone settings":     locales.RuleSettingsPhone,
	"Every action":       locales.RuleEveryAction,
	"Blocked by you":     locales.RuleBlockedByYou,
	"credentials":        locales.RuleCredentials,
	"Read-only":          locales.RuleReadOnly,
	"Task apps":          locales.RuleTaskApps,
	"Agent scope":        locales.RuleAgentScope,
	"Finance apps":       locales.RuleFinanceApps,
	"Password managers":  locales.RulePasswordManagers,
	"Unsafe links":       locales.RuleUnsafeLinks,
	"Files":              locales.RuleFiles,
	"Recordings":         locales.RuleRecordings,
	"App logs":           locales.RuleAppLogs,
	"Agent request":      locales.RuleAgentRequest,

	"Installing from the computer": locales.RuleInstallFromComputer,
	"Uninstalling apps":            locales.RuleUninstallApps,
	"Pinky's own apps":             locales.RulePinkyApps,
	"System apps":                  locales.RuleSystemApps,
}

// ruleName names a rule as the person reads it: "Payments". A rule of the
// person's own rules file keeps its name.
func ruleName(rule string) string {
	if f := ruleNames[rule]; f != nil {
		return f()
	}
	return rule
}

// ruleActions are what the presets' rules cover, for "Always allow …".
var ruleActions = map[string]func() string{
	"Payments":           locales.RuleActionPayments,
	"Messages":           locales.RuleActionMessages,
	"Commits":            locales.RuleActionCommits,
	"Installs and links": locales.RuleActionInstallsLinks,
	"Calls":              locales.RuleActionCalls,
	"Settings changes":   locales.RuleActionSettingsChanges,
	"Every action":       locales.RuleActionEveryAction,
	"Agent request":      locales.RuleActionAgentRequest,
}

// ruleAction says what a rule covers, inside "Always allow payments in
// Dinely": "payments", or "this kind" without a rule.
func ruleAction(rule string) string {
	if rule == "" {
		return locales.RuleActionThisKind()
	}
	if f := ruleActions[rule]; f != nil {
		return f()
	}
	return locales.RuleActionOther(lowerFirst(rule))
}

// joinAnd joins names for a sentence: "Dinely, Chatter and Calendar".
func joinAnd(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	case 2:
		return locales.ListPair(s[0], s[1])
	}
	return locales.ListMany(strings.Join(s[:len(s)-1], locales.ListSeparator()), s[len(s)-1])
}

// The language the app shows is MyGo's: the one of the catalogs' languages
// the person reads best, by the system's preferred languages, unless they
// pick one in Settings › Advanced › Language. MyGo shows a pick at once and
// keeps it (on macOS as the app's language, which System Settings shows
// too). It answers on its main thread, which tests have none of, so they
// replace these.
var (
	setAppLocale = mygo.App.SetLocale
	appLocale    = mygo.App.Locale
	appPreferred = mygo.App.PreferredLanguages
)

// langPicked is the language picked in Settings this run, "" for the
// system's, and langPickedSet whether one was.
var (
	langPicked    string
	langPickedSet bool
)

// languageChoices are the language select's tags, "" for the system's,
// and their labels, each language named in itself.
func languageChoices() (tags, labels []string) {
	tags, labels = []string{""}, []string{locales.SettingsLanguageSystem()}
	for _, tag := range mygo.App.Languages() {
		tags = append(tags, tag)
		labels = append(labels, i18n.Autonym(tag))
	}
	return tags, labels
}

// languageNow is the select's tag: the one picked this run, else the
// system's ("") unless the app shows another language than the system's
// would be, as a pick of an earlier run makes it.
func languageNow() string {
	if langPickedSet {
		return langPicked
	}
	langs := mygo.App.Languages()
	sys := i18n.Match(appPreferred(), langs)
	if sys == "" && len(langs) > 0 {
		sys = langs[0]
	}
	if cur := appLocale(); cur != sys {
		return cur
	}
	return ""
}

// pickLanguage shows the app in tag, "" for the system's language.
func pickLanguage(tag string) {
	langPicked, langPickedSet = tag, true
	setAppLocale(tag)
}

// languageRow is Settings › Advanced › Language.
func (a *app) languageRow(c *mg.Context) {
	tags, labels := languageChoices()
	now := languageNow()
	choice := labels[0]
	for i, t := range tags {
		if t == now {
			choice = labels[i]
		}
	}
	settingRow(c, locales.SettingsLanguage(), locales.SettingsLanguageDesc(), false, func() {
		if mg.Select(c.Key("language"), &choice, labels).Label(locales.SettingsLanguage()).Changed() {
			for i, l := range labels {
				if l == choice {
					pickLanguage(tags[i])
				}
			}
		}
	})
}
