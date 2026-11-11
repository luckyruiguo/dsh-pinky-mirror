//go:build e2e

package ui

import "github.com/egoist/mygo"

// In test builds a Debug menu opens the windows that the automation
// socket cannot reach otherwise: the menu bar flyout and the approval
// panel.
func init() {
	debugMenu = func(a *app) *mygo.MenuItem {
		return &mygo.MenuItem{Label: "Debug", Submenu: []*mygo.MenuItem{
			{Label: "Open Flyout", Click: func(*mygo.MenuItem, *mygo.Window) { a.toggleFlyout() }},
			{Label: "Show Approval Panel", Click: func(*mygo.MenuItem, *mygo.Window) { a.showPanel(false) }},
			{Label: "Hide Main Window", Click: func(*mygo.MenuItem, *mygo.Window) { a.main.Hide() }},
			{Label: "Show Main Window", Click: func(*mygo.MenuItem, *mygo.Window) { a.showMain() }},
		}}
	}
}
