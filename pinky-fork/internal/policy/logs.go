package policy

import (
	"github.com/DIYgod/pinky/internal/screen"
)

// An app's logs and crash reports (phone_logs) can hold more than its
// screen shows: account names, addresses, tokens a careless app logs. So
// the first read of an app in a task asks the person under RuleAppLogs,
// unless the app is a development build, and some apps are never read at
// all (LogsRefused).

// RuleAppLogs is the rule the first read of an app's logs in a task asks
// under, and the rule a refused read reports when no app rule names why.
const RuleAppLogs = "App logs"

// Rule names of the bundled rules that LogsRefused reports when the rule
// itself is off: an app of these categories is never read, whatever the
// rules say about opening it.
const (
	ruleFinanceApps      = "Finance apps"
	rulePasswordManagers = "Password managers"
)

// LogsRefused reports whether an app's logs and crash reports must never
// reach an agent, and names the rule that says so: an app a rule blocks
// (even one the person allowed for the task, since that allows its screen
// only), a finance app or password manager (even with their rules turned
// off), and the phone's own system apps, whose logs mix in everyone's.
func (e *Engine) LogsRefused(app screen.App) (rule string, refused bool) {
	if rule, blocked := e.Blocked(app); blocked {
		return rule, true
	}
	cats := e.Categories(app)
	switch {
	case contains(cats, CatPasswords):
		return rulePasswordManagers, true
	case contains(cats, CatFinance):
		return ruleFinanceApps, true
	case contains(cats, CatSystem), contains(cats, CatSettings), contains(cats, CatPhone):
		return RuleAppLogs, true
	}
	return "", false
}
