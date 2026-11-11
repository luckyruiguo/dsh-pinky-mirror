package ui

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/secret"
)

// askState is Ask Pinky's: the box at the top of Tasks that gives Pinky's
// own agent a task (Options.Pilot), and its part of Settings › Agents.
type askState struct {
	draft  string // the task being typed
	device string // the phone chosen on All phones
	gen    int    // bumped on each start, to start the box afresh
	err    string // why the last start failed
	errFor string // the phone it was for

	// keys says which services have an API key saved, by provider, once
	// looked up off the main thread.
	keys     map[string]bool
	checking map[string]bool
	// rechecked is when the last task that ended for want of a working
	// key ended, so that its note has the keychain asked once.
	rechecked time.Time

	// Settings › Agents › Ask Pinky.
	editing string // the editor open: "key", "model" or "url"
	value   string // what is typed in it
	verr    string // why it was not saved
	busy    bool   // the key is on its way to the keychain
	scroll  bool   // bring the settings into view, from the box's setup link
}

// The editors of Ask Pinky's settings.
const (
	askEditKey   = "key"
	askEditModel = "model"
	askEditURL   = "url"
)

// claudeModels are the models Settings offers for Anthropic's own API,
// the most capable first.
var claudeModels = []struct{ id, name string }{
	{"claude-opus-5-5", "Claude Opus 5.5"},
	{"claude-sonnet-5-5", "Claude Sonnet 5.5"},
	{"claude-haiku-5-5", "Claude Haiku 5.5"},
}

// modelName is a model as the person reads it: a Claude model's name, or
// the name the service gave.
func modelName(cfg pilot.Config) string {
	if cfg.Provider == pilot.Anthropic {
		for _, m := range claudeModels {
			if m.id == cfg.Model {
				return m.name
			}
		}
	}
	return cfg.Model
}

// askKey reports whether an API key is saved for a provider, and whether
// that is known yet: the keychain is asked once, off the main thread.
func (a *app) askKey(provider string) (has, known bool) {
	st := &a.ask
	if st.keys == nil {
		st.keys, st.checking = map[string]bool{}, map[string]bool{}
	}
	if has, ok := st.keys[provider]; ok {
		return has, true
	}
	if !st.checking[provider] {
		st.checking[provider] = true
		store := a.secrets()
		go func() {
			has := store.Has(secret.ModelKeyAccount(provider))
			a.post(func() {
				st.checking[provider] = false
				st.keys[provider] = has
			})
		}()
	}
	return false, false
}

// forgetKey has the keychain asked again, as after the API refused the
// key or found none.
func (a *app) forgetKey(provider string) {
	if a.ask.keys != nil && !a.ask.checking[provider] {
		delete(a.ask.keys, provider)
	}
}

// askReady reports whether Ask Pinky can start a task: a key is saved and
// a model chosen. known is false while the keychain has not answered.
func (a *app) askReady() (ready, known bool) {
	cfg := a.settings.PilotConfig()
	has, known := a.askKey(cfg.Provider)
	return has && cfg.Model != "", known
}

// openAskSettings shows Ask Pinky's part of Settings › Agents.
func (a *app) openAskSettings() {
	a.page, a.set.sec = pageSettings, secAgents
	a.ask.scroll = true
}

// askPhone is the phone the box gives a task to: the one shown, or on All
// phones the one chosen in the box.
func (a *app) askPhone() (gateway.DeviceView, bool) {
	if d := a.device(a.sel); d != nil {
		return *d, true
	}
	if d := a.device(a.ask.device); d != nil {
		return *d, true
	}
	if len(a.devices) == 0 {
		return gateway.DeviceView{}, false
	}
	a.ask.device = a.devices[0].Info.ID
	return a.devices[0], true
}

// askBox is the box at the top of Tasks: a task for Pinky's own agent
// and Start; while it works, how far it is and Stop; once it ended, how.
func (a *app) askBox(c *mg.Context) {
	if a.opt.Pilot == nil || !a.settings.AskPinky {
		return
	}
	d, ok := a.askPhone()
	if !ok {
		return
	}
	p := pal(c)
	st := &a.ask
	id := d.Info.ID
	ps, has := a.opt.Pilot.Status(id)
	card(c, mg.Column(c.Key("ask")).Padding(14)).Gap(sp3).Label(locales.AskTitle()).Children(func() {
		if has && ps.Running() {
			a.askRunning(c, ps)
			return
		}
		if has {
			a.askEnded(c, ps)
		}
		ready, known := a.askReady()
		if !known {
			return
		}
		if !ready {
			a.askSetup(c)
			return
		}
		if a.sel == allPhones || a.device(a.sel) == nil {
			a.askPicker(c, id)
		}
		cfg := a.settings.PilotConfig()
		area := mg.TextArea(c.Key(fmt.Sprintf("ask-%d", st.gen)), &st.draft).Label(locales.AskField()).
			Placeholder(locales.AskPlaceholder(d.Info.Name)).Lines(2, 6)
		why := ""
		if t := a.taskOf(id); t != nil && running(t.State) {
			why = locales.AskBusy(nonEmpty(t.Agent, locales.AskAnotherAgent()), d.Info.Name)
		} else if d.Status == gateway.DeviceOffline {
			why = locales.PhoneOffline(d.Info.Name)
		}
		if why != "" {
			mg.Text(c, why).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2).Role(mg.RoleStatus)
		}
		if st.err != "" && st.errFor == id {
			mg.Text(c, st.err).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus)
		}
		// The model and the limit, on a line each, so that they fit beside
		// Start in the panel's width.
		mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mg.Text(c, modelName(cfg)).FontSize(fsCaption).FontWeight(fwMedium).TextColor(p.ink2).SingleLine()
				mg.Text(c, locales.AskTaskLimit(limitText(cfg))).FontSize(fsCaption).TextColor(p.ink3).FontFeatures("tnum")
			})
			can := why == "" && strings.TrimSpace(st.draft) != ""
			start := button(c, locales.AskStart(), btnOpt{kind: btnPrimary, small: true}).Disabled(!can)
			if can && (start.Clicked() || area.Shortcut(mg.Cmd, mg.KeyEnter)) {
				a.askStart(id)
			}
		})
	})
}

// askPicker chooses the phone on All phones.
func (a *app) askPicker(c *mg.Context, id string) {
	labels := make([]string, len(a.devices))
	seen := map[string]int{}
	for i, d := range a.devices {
		l := d.Info.Name
		if seen[l]++; seen[l] > 1 {
			l = fmt.Sprintf("%s (%d)", l, seen[l])
		}
		labels[i] = l
	}
	cur := ""
	for i, d := range a.devices {
		if d.Info.ID == id {
			cur = labels[i]
		}
	}
	if mg.Select(c.Key("ask-phone"), &cur, labels).Label(locales.AskPhone()).Changed() {
		for i, l := range labels {
			if l == cur {
				a.ask.device = a.devices[i].Info.ID
			}
		}
	}
}

func (a *app) askStart(id string) {
	st := &a.ask
	err := a.opt.Pilot.Start(a.settings.PilotConfig(), id, st.draft)
	switch {
	case err == nil:
		st.draft, st.err, st.errFor = "", "", ""
		st.gen++
	case errors.Is(err, pilot.ErrNoTask):
	case errors.Is(err, pilot.ErrNoModel):
		a.openAskSettings()
	default:
		st.err, st.errFor = locales.AskErrStart(err.Error()), id
	}
}

// askRunning is the box while Pinky works: how far it is, and Stop.
func (a *app) askRunning(c *mg.Context, s pilot.Status) {
	p := pal(c)
	c.After(time.Second)
	mg.Column(c).Gap(2).Children(func() {
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			dot(c, p.green, true)
			mg.Text(c, locales.AskWorking()).FontWeight(fwSemibold).Grow(1).MinWidth(0)
			if button(c, locales.TasksStop(), btnOpt{small: true, kind: btnRed}).Label(locales.AskStop()).Clicked() {
				a.stop(s.Device)
			}
		})
		// How far it is, under the words and across the card, so that it
		// fits on a line in every language.
		mg.Text(c, usageText(s, c.Now())).FontSize(fsCaption).TextColor(p.ink2).FontFeatures("tnum").Margin(0, 0, 0, 8+sp2)
	})
}

// askEnded says how Pinky's last task on the phone ended, until the
// person dismisses it or starts another.
func (a *app) askEnded(c *mg.Context, s pilot.Status) {
	p := pal(c)
	title, reason := pilotEnd(s)
	if (s.End == pilot.NoKey || s.End == pilot.BadKey) && !s.Ended.Equal(a.ask.rechecked) {
		// The key may have changed since: ask the keychain again, once.
		a.ask.rechecked = s.Ended
		a.forgetKey(s.Config.Provider)
	}
	mg.Column(c).Padding(sp3).Radius(rControl).Background(p.bg2).Gap(sp1).Role(mg.RoleStatus).Children(func() {
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			switch s.End {
			case pilot.Done:
				mg.Icon(c, icCheck).FontSize(16).TextColor(p.green)
			case pilot.Stopped:
				dot(c, p.ink3, false)
			default:
				dot(c, p.amberDot, false)
			}
			mg.Text(c, title).FontWeight(fwSemibold).Grow(1).MinWidth(0)
			if quietButton(c, locales.AskDismiss()).Clicked() {
				a.opt.Pilot.Dismiss(s.Device)
			}
		})
		if reason != "" {
			mg.Text(c, reason).FontSize(fsSmall).LineHeight(1.4).Selectable()
		}
		mg.Text(c, usageText(s, c.Now())).FontSize(fsCaption).TextColor(p.ink3).FontFeatures("tnum")
	})
}

// askSetup is the box while Ask Pinky lacks a key or a model.
func (a *app) askSetup(c *mg.Context) {
	p := pal(c)
	cfg := a.settings.PilotConfig()
	link := locales.AskSetupKey()
	if has, _ := a.askKey(cfg.Provider); has && cfg.Model == "" {
		link = locales.AskSetupModel()
	}
	mg.Column(c).Gap(sp1).Children(func() {
		mg.Text(c, locales.AskTitle()).FontSize(fsHeadline).FontWeight(fwSemibold)
		mg.Text(c, locales.AskSetup()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		if linkButton(c, link).AlignSelf(mg.Start).Margin(sp1, 0, 0, 0).Clicked() {
			a.openAskSettings()
		}
	})
}

// usageText is how much a task did and used: "12 steps · US$0.18 · 2 min".
func usageText(s pilot.Status, now time.Time) string {
	parts := []string{locales.AskSteps(s.Steps)}
	switch {
	case s.Priced:
		parts = append(parts, dollars(s.Cost))
	case s.Tokens > 0:
		parts = append(parts, locales.AskTokens(int(s.Tokens)))
	}
	end := s.Ended
	if end.IsZero() {
		end = now
	}
	return strings.Join(append(parts, elapsed(end.Sub(s.Started))), " · ")
}

// limitText is a task's cost limit: in dollars, or in tokens for a model
// whose prices Pinky does not know.
func limitText(cfg pilot.Config) string {
	if !cfg.Priced() {
		return locales.AskTokens(int(cfg.TokenLimit()))
	}
	return dollars(cfg.MaxCost)
}

func dollars(v float64) string { return locales.AskDollars(strconv.FormatFloat(v, 'f', 2, 64)) }

// pilotEnd is how a task of Pinky's own agent ended, in the language
// shown: a title ("Stopped") and, when there is more to say, why.
func pilotEnd(s pilot.Status) (title, reason string) {
	return pilotEndIn(locales.In(i18n.Locale()), s)
}

func pilotEndIn(m locales.Messages, s pilot.Status) (title, reason string) {
	switch s.End {
	case pilot.Done:
		return m.AskEndDone(), s.Summary
	case pilot.Stopped:
		return m.AskEndStopped(), ""
	case pilot.StepLimit:
		return m.AskEndStopped(), m.AskLimitSteps(s.Config.MaxSteps)
	case pilot.TimeLimit:
		return m.AskEndStopped(), m.AskLimitTime(int(s.Config.MaxTime / time.Minute))
	case pilot.CostLimit:
		if s.Priced {
			return m.AskEndStopped(), m.AskLimitCost(m.AskDollars(strconv.FormatFloat(s.Config.MaxCost, 'f', 2, 64)))
		}
		return m.AskEndStopped(), m.AskLimitTokens(int(s.Config.TokenLimit()))
	case pilot.Refused:
		return m.AskEndFailed(), m.AskRefused()
	case pilot.NoKey:
		return m.AskEndNotStarted(), m.AskNoKey()
	case pilot.BadKey:
		return m.AskEndNotStarted(), m.AskBadKey()
	}
	return m.AskEndFailed(), s.Summary
}

// DescribePilot is the line Pinky's own agent leaves in Activity for a
// task it ends itself, in the app's language (pilot.Options.Describe).
func DescribePilot(s pilot.Status) string {
	m := locales.In(i18n.Locale())
	title, reason := pilotEndIn(m, s)
	if reason == "" {
		return title
	}
	return m.AskEndLine(title, reason)
}

// askSettings is Ask Pinky's part of Settings › Agents: whether the box
// shows, the service, its API key, model and address, and the limits.
func (a *app) askSettings(c *mg.Context) {
	if a.opt.Pilot == nil {
		return
	}
	st := &a.ask
	s := &a.settings
	cfg := s.PilotConfig()
	col := mg.Column(c.Key("ask-settings")).Gap(10).Label(locales.AskTitle())
	if st.scroll {
		st.scroll = false
		col.ScrollIntoView()
	}
	col.Children(func() {
		settingsHeading(c, locales.AskTitle(), locales.SettingsAskIntro())
		group(c).Children(func() {
			a.toggleRow(c, "ask-on", locales.SettingsAskOn(), locales.SettingsAskOnDesc(), false, &s.AskPinky)
			settingRow(c, locales.SettingsAskService(), locales.SettingsAskServiceDesc(), false, func() {
				labels := []string{locales.SettingsAskServiceAnthropic(), locales.SettingsAskServiceOpenAI()}
				cur := labels[0]
				if cfg.Provider == pilot.OpenAI {
					cur = labels[1]
				}
				if mg.Select(c.Key("ask-service"), &cur, labels).Label(locales.SettingsAskService()).Changed() {
					s.PilotProvider = pilot.Anthropic
					if cur == labels[1] {
						s.PilotProvider = pilot.OpenAI
					}
					st.editing, st.value, st.verr = "", "", ""
					a.changed()
				}
			})
			a.askKeyRow(c, cfg)
			a.askModelRow(c, cfg)
			a.askURLRow(c, cfg)
			a.askLimitRows(c, cfg)
		})
	})
}

func (a *app) askKeyRow(c *mg.Context, cfg pilot.Config) {
	p := pal(c)
	st := &a.ask
	store := a.secrets()
	if !secret.Supported(store) {
		settingRow(c, locales.SettingsAskKey(), locales.SettingsAskKeyUnsupported(), false, nil)
		return
	}
	has, known := a.askKey(cfg.Provider)
	if st.editing == askEditKey {
		a.askEditor(c, locales.SettingsAskKey(), keyKeep(), true, locales.SettingsAskKeyPlaceholder(), func(v string) {
			a.saveKey(cfg.Provider, v)
		})
		return
	}
	settingRow(c, locales.SettingsAskKey(), keyKeep(), false, func() {
		if !known {
			return
		}
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Shrink(0).Children(func() {
			if has {
				mg.Row(c).Gap(6).AlignItems(mg.Center).Margin(0, sp1, 0, 0).Children(func() {
					mg.Icon(c, icLock).FontSize(14).TextColor(p.ink2)
					mg.Text(c, locales.SettingsAskKeySaved()).FontSize(fsSmall).TextColor(p.ink2)
				})
				if button(c, locales.SettingsChange(), btnOpt{small: true}).Label(locales.SettingsAskKeyChange()).Clicked() {
					st.editing, st.value, st.verr = askEditKey, "", ""
				}
				if button(c, locales.SettingsAskKeyRemove(), btnOpt{small: true, kind: btnRed}).Label(locales.SettingsAskKeyRemoveLabel()).Clicked() {
					a.removeKey(cfg.Provider)
				}
				return
			}
			if button(c, locales.SettingsAskKeyAdd(), btnOpt{small: true, kind: btnPrimary}).Label(locales.SettingsAskKeyAddLabel()).Clicked() {
				st.editing, st.value, st.verr = askEditKey, "", ""
			}
		})
	})
	if st.verr != "" && st.editing == "" {
		mg.Text(c, st.verr).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus).Margin(0, sp4, sp2, sp4)
	}
}

// keyKeep says where the key stays, in the system's words.
func keyKeep() string {
	switch runtime.GOOS {
	case "darwin":
		return locales.SettingsAskKeyKeepMac()
	case "windows":
		return locales.SettingsAskKeyKeepWindows()
	}
	return locales.SettingsAskKeyKeepOther()
}

// saveKey saves an API key in the keychain, off the main thread. The key
// leaves the client's state at once and is never read back.
func (a *app) saveKey(provider, key string) {
	st := &a.ask
	key = strings.TrimSpace(key)
	st.value, st.verr, st.busy = "", "", true
	st.gen++
	store := a.secrets()
	go func() {
		err := store.Save(secret.ModelKeyAccount(provider), key)
		a.post(func() {
			st.busy = false
			if err != nil {
				st.verr = locales.SettingsAskKeyErrSave(err.Error())
				return
			}
			st.keys[provider], st.editing = true, ""
			a.toasts = append(a.toasts, locales.SettingsAskKeySavedToast())
		})
	}()
}

func (a *app) removeKey(provider string) {
	st := &a.ask
	store := a.secrets()
	go func() {
		err := store.Delete(secret.ModelKeyAccount(provider))
		a.post(func() {
			if err != nil && !errors.Is(err, secret.ErrNotFound) {
				st.verr = locales.SettingsAskKeyErrRemove(err.Error())
				return
			}
			st.keys[provider], st.verr = false, ""
			a.toasts = append(a.toasts, locales.SettingsAskKeyRemovedToast())
		})
	}()
}

func (a *app) askModelRow(c *mg.Context, cfg pilot.Config) {
	st := &a.ask
	s := &a.settings
	set := func(v string) {
		if cfg.Provider == pilot.OpenAI {
			s.PilotOpenAIModel = v
		} else {
			s.PilotAnthropicModel = v
		}
		a.changed()
	}
	if cfg.Provider == pilot.Anthropic && cfg.BaseURL == "" {
		// Anthropic's own API: its models to choose from.
		settingRow(c, locales.SettingsAskModel(), locales.SettingsAskModelClaude(), false, func() {
			var labels []string
			cur := ""
			for _, m := range claudeModels {
				labels = append(labels, m.name)
				if m.id == cfg.Model {
					cur = m.name
				}
			}
			if cur == "" {
				cur = cfg.Model
				labels = append(labels, cfg.Model)
			}
			if mg.Select(c.Key("ask-model"), &cur, labels).Label(locales.SettingsAskModel()).Changed() {
				for _, m := range claudeModels {
					if m.name == cur {
						set(m.id)
					}
				}
			}
		})
		return
	}
	if st.editing == askEditModel {
		a.askEditor(c, locales.SettingsAskModel(), locales.SettingsAskModelDesc(), false, locales.SettingsAskModelPlaceholder(), func(v string) {
			set(strings.TrimSpace(v))
			st.editing = ""
		})
		return
	}
	a.askValueRow(c, locales.SettingsAskModel(), locales.SettingsAskModelDesc(), nonEmpty(cfg.Model, locales.SettingsAskModelNone()),
		locales.SettingsAskModelChange(), askEditModel, cfg.Model)
}

func (a *app) askURLRow(c *mg.Context, cfg pilot.Config) {
	st := &a.ask
	s := &a.settings
	desc := locales.SettingsAskURLDescAnthropic()
	if cfg.Provider == pilot.OpenAI {
		desc = locales.SettingsAskURLDescOpenAI()
	}
	if st.editing == askEditURL {
		a.askEditor(c, locales.SettingsAskURL(), desc, false, locales.SettingsAskURLDefault(), func(v string) {
			v = strings.TrimRight(strings.TrimSpace(v), "/")
			if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
				st.verr = locales.SettingsAskURLErr()
				return
			}
			if cfg.Provider == pilot.OpenAI {
				s.PilotOpenAIURL = v
			} else {
				s.PilotAnthropicURL = v
			}
			st.editing = ""
			a.changed()
		})
		return
	}
	a.askValueRow(c, locales.SettingsAskURL(), desc, nonEmpty(cfg.BaseURL, locales.SettingsAskURLDefault()),
		locales.SettingsAskURLChange(), askEditURL, cfg.BaseURL)
}

// askValueRow is a setting with its value and Change.
func (a *app) askValueRow(c *mg.Context, title, desc, value, changeLabel, editor, current string) {
	p := pal(c)
	st := &a.ask
	settingRow(c, title, desc, false, func() {
		mg.Row(c).Gap(10).AlignItems(mg.Center).Shrink(1).MinWidth(0).Children(func() {
			mg.Text(c, value).FontSize(fsSmall).TextColor(p.ink2).SingleLine().Shrink(1).MinWidth(0).MaxWidth(260)
			if button(c, locales.SettingsChange(), btnOpt{small: true}).Label(changeLabel).Clicked() {
				st.editing, st.value, st.verr = editor, current, ""
			}
		})
	})
}

// askEditor is a setting being edited: a field with Save and Cancel.
func (a *app) askEditor(c *mg.Context, title, desc string, password bool, placeholder string, save func(string)) {
	p := pal(c)
	st := &a.ask
	groupRow(c, mg.Column(c).Gap(sp3), false).Children(func() {
		rowWords(c, mg.Column(c), title, desc)
		in := mg.TextInput(c.Key(fmt.Sprintf("ask-edit-%s-%d", st.editing, st.gen)), &st.value).Label(title).
			Placeholder(placeholder).AutoFocus().MaxWidth(420)
		if password {
			in.Password()
		}
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			empty := password && strings.TrimSpace(st.value) == ""
			ok := button(c, locales.SettingsSave(), btnOpt{small: true, kind: btnPrimary}).Disabled(st.busy || empty)
			if (ok.Clicked() || in.Submitted()) && !st.busy && !empty {
				save(st.value)
			}
			if button(c, locales.SettingsCancel(), btnOpt{small: true}).Clicked() {
				st.editing, st.value, st.verr = "", "", ""
				st.gen++
			}
			if st.busy {
				spinner(c)
			}
		})
		if st.verr != "" {
			mg.Text(c, st.verr).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}

// The limits Settings offers; one from the settings file that is not
// among them is offered too.
var (
	stepChoices   = []int{25, 50, 100, 200, 500}
	minuteChoices = []int{5, 10, 20, 30, 60}
	costChoices   = []float64{0.5, 1, 2, 5, 10}
)

func (a *app) askLimitRows(c *mg.Context, cfg pilot.Config) {
	s := &a.settings
	limitRow(c, "ask-steps", locales.SettingsAskSteps(), locales.SettingsAskStepsDesc(), false,
		stepChoices, cfg.MaxSteps, func(v int) string { return locales.AskSteps(v) },
		func(v int) { s.PilotMaxSteps = v; a.changed() })
	limitRow(c, "ask-time", locales.SettingsAskTime(), locales.SettingsAskTimeDesc(), false,
		minuteChoices, int(cfg.MaxTime/time.Minute), func(v int) string { return locales.AskMinutes(v) },
		func(v int) { s.PilotMaxMinutes = v; a.changed() })
	desc := locales.SettingsAskCostDesc()
	if !cfg.Priced() {
		desc = locales.SettingsAskCostDescTokens()
	}
	limitRow(c, "ask-cost", locales.SettingsAskCost(), desc, true,
		costChoices, cfg.MaxCost, dollars,
		func(v float64) { s.PilotMaxCost = v; a.changed() })
}

// limitRow is a limit chosen from choices.
func limitRow[T comparable](c *mg.Context, key, title, desc string, last bool, choices []T, cur T, label func(T) string, set func(T)) {
	settingRow(c, title, desc, last, func() {
		found := false
		for _, v := range choices {
			found = found || v == cur
		}
		if !found {
			choices = append([]T{cur}, choices...)
		}
		labels := make([]string, len(choices))
		for i, v := range choices {
			labels[i] = label(v)
		}
		sel := label(cur)
		if mg.Select(c.Key(key), &sel, labels).Label(title).Changed() {
			for i, l := range labels {
				if l == sel {
					set(choices[i])
				}
			}
		}
	})
}
