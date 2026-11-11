package android

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Links: phone_launch opens web links and apps' own links ("myapp://…")
// with a VIEW intent, as a tap on the link in a browser would. The gateway
// judges the app the link opens in before anything opens, so LinkApp must
// name the app the phone itself would pick, and OpenLink opens the link in
// that app and nowhere else: it starts the app's activity by name, so a
// link the phone would offer to several apps never reaches one the rules
// did not see.

// viewIntent is what a tap on a link in a browser starts: Android only
// starts an activity for it when its intent filter has both categories.
const viewIntent = "-a android.intent.action.VIEW -c android.intent.category.DEFAULT -c android.intent.category.BROWSABLE"

const candidatesSection = "::pinky-candidates"

// linkComponentRE is an activity as `cmd package … --brief` prints it. The
// resolver's package is "android", without a dot.
var linkComponentRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*)/([A-Za-z0-9_.$]+)$`)

// linkArg quotes a link for the shell, refusing what no link holds.
func linkArg(link string) (string, error) {
	link = strings.TrimSpace(link)
	if link == "" || strings.ContainsFunc(link, unicode.IsControl) {
		return "", fmt.Errorf("android: open link %q: not a link: %w", link, driver.ErrUnsupported)
	}
	return adb.Quote(link), nil
}

// linkQuery is the shell command that asks, in a user ("current" or an
// id), which activity the phone resolves a link to and which activities
// can open it.
func linkQuery(arg, user string) string {
	q := func(what string) string {
		return fmt.Sprintf("cmd package %s --brief --user %s %s -d %s 2>&1", what, user, viewIntent, arg)
	}
	return q("resolve-activity") + "; echo '" + candidatesSection + "'; " + q("query-activities")
}

// parseLinkQuery reads linkQuery's output: the activity the phone resolves
// the link to ("" when none) and the activities that can open it, in the
// order Android ranks them, as "package/class".
func parseLinkQuery(out string) (resolved string, candidates []string, err error) {
	out = strings.ReplaceAll(out, "\r", "")
	head, tail, ok := strings.Cut(out, candidatesSection)
	if !ok {
		return "", nil, fmt.Errorf("unexpected output: %q: %w", firstLine([]byte(out)), driver.ErrUnsupported)
	}
	comps := func(s string) []string {
		var cs []string
		for _, line := range strings.Split(s, "\n") {
			m := linkComponentRE.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil {
				continue
			}
			cls := m[2]
			if strings.HasPrefix(cls, ".") {
				cls = m[1] + cls
			}
			c := m[1] + "/" + cls
			if !slices.Contains(cs, c) {
				cs = append(cs, c)
			}
		}
		return cs
	}
	if r := comps(head); len(r) > 0 {
		resolved = r[0]
	} else if !strings.Contains(head, "No activity found") {
		return "", nil, fmt.Errorf("finding the app for a link: %q: %w", firstLine([]byte(head)), driver.ErrUnsupported)
	}
	candidates = comps(tail)
	if len(candidates) == 0 && !strings.Contains(tail, "No activities found") {
		return "", nil, fmt.Errorf("finding the apps for a link: %q: %w", firstLine([]byte(tail)), driver.ErrUnsupported)
	}
	return resolved, candidates, nil
}

func componentPackage(c string) string {
	pkg, _, _ := strings.Cut(c, "/")
	return pkg
}

// pickLinkHandler returns the package a link opens in: the one the phone
// resolves it to when that is one of the apps that can open it (the
// person's default, or an app that verified the link's site), or the only
// app that can. Otherwise the phone would ask which app to use, in its
// resolver or chooser, whatever the maker calls it, and offered lists the
// packages it would offer; none of either means no app opens the link.
func pickLinkHandler(resolved string, candidates []string) (pkg string, offered []string) {
	for _, c := range candidates {
		if p := componentPackage(c); !slices.Contains(offered, p) {
			offered = append(offered, p)
		}
	}
	if resolved != "" && slices.Contains(candidates, resolved) {
		return componentPackage(resolved), nil
	}
	if len(offered) == 1 {
		return offered[0], nil
	}
	return "", offered
}

// LinkApp implements driver.LinkOpener: the app the phone opens a link in,
// for the person in front, as a tap on the link would.
func (d *Device) LinkApp(ctx context.Context, link string) (driver.App, error) {
	arg, err := linkArg(link)
	if err != nil {
		return driver.App{}, err
	}
	out, err := d.client.Shell(ctx, d.info.ID, linkQuery(arg, "current"))
	if err != nil {
		return driver.App{}, wrapErr(err)
	}
	resolved, candidates, err := parseLinkQuery(string(out))
	if err != nil {
		return driver.App{}, fmt.Errorf("android: open link: %w", err)
	}
	pkg, offered := pickLinkHandler(resolved, candidates)
	_, _ = d.cachedApps(ctx, 0) // for the apps' names
	switch {
	case pkg != "":
		return d.packageApp(pkg), nil
	case len(offered) == 0:
		return driver.App{}, fmt.Errorf("android: no app opens %s: %w", link, driver.ErrNotFound)
	}
	choice := &driver.LinkChoiceError{Link: link}
	for _, p := range offered {
		choice.Apps = append(choice.Apps, d.packageApp(p))
	}
	return driver.App{}, choice
}

// packageApp is the app of a package as the app list names it, or by its
// package when it has no launcher entry.
func (d *Device) packageApp(pkg string) driver.App {
	d.appsMu.Lock()
	defer d.appsMu.Unlock()
	for _, a := range d.apps {
		if a.ID == pkg {
			return a
		}
	}
	return driver.App{ID: pkg, Name: systemNames[pkg]}
}

// OpenLink implements driver.LinkOpener: the link opens in app, a package
// or an app clone, on the main screen.
func (d *Device) OpenLink(ctx context.Context, link string, app driver.App) error {
	t, err := d.linkTarget(ctx, app)
	if err != nil {
		return err
	}
	return d.openLink(ctx, link, t, 0)
}

// linkTarget is the app OpenLink opens a link in.
func (d *Device) linkTarget(ctx context.Context, app driver.App) (launchTarget, error) {
	if _, _, ok := driver.ParseCloneID(app.ID); !ok {
		if !looksLikePackage(app.ID) {
			return launchTarget{}, fmt.Errorf("android: open link in %q: not a package: %w", app.ID, driver.ErrNotFound)
		}
		return launchTarget{pkg: app.ID}, nil
	}
	if _, err := d.cachedApps(ctx, 0); err != nil {
		return launchTarget{}, err
	}
	d.appsMu.Lock()
	c, ok := d.clones[app.ID]
	d.appsMu.Unlock()
	if !ok {
		return launchTarget{}, fmt.Errorf("android: open link in %q: no such app clone: %w", app.ID, driver.ErrNotFound)
	}
	return launchTarget{pkg: c.pkg, clone: &c}, nil
}

// openLink opens a link in an app on a display: it starts the app's own
// activity for the link, in the app's user, by name.
func (d *Device) openLink(ctx context.Context, link string, t launchTarget, display int) error {
	arg, err := linkArg(link)
	if err != nil {
		return err
	}
	user := "current"
	if t.clone != nil {
		user = strconv.Itoa(t.clone.user)
	}
	out, err := d.client.Shell(ctx, d.info.ID, linkQuery(arg, user))
	if err != nil {
		return wrapErr(err)
	}
	_, candidates, err := parseLinkQuery(string(out))
	if err != nil {
		return fmt.Errorf("android: open link: %w", err)
	}
	var component string
	for _, c := range candidates {
		if componentPackage(c) == t.pkg {
			component = c
			break
		}
	}
	what := fmt.Sprintf("open %s in %s", link, t.pkg)
	if t.clone != nil {
		what += fmt.Sprintf(" in user %d", t.clone.user)
	}
	if component == "" || !linkComponentRE.MatchString(component) {
		return fmt.Errorf("android: %s: the app does not open this link: %w", what, driver.ErrNotFound)
	}
	// FLAG_ACTIVITY_NEW_TASK, as a browser starts an app for a link.
	args := fmt.Sprintf("%s -f 0x10000000 -d %s -n '%s'", viewIntent, arg, component)
	if err := d.amStart(ctx, user, display, args, what); err != nil {
		return err
	}
	d.cfg.logf("android: %s: opened a link in %s (user %s) on display %d", d.info.ID, component, user, display)
	return nil
}

// amStart runs `am start` with args in a user ("current" or an id) on a
// display, and reports its failure as what failed; an activity that is no
// longer there is driver.ErrNotFound.
func (d *Device) amStart(ctx context.Context, user string, display int, args, what string) error {
	start := func(withDisplay bool) (string, error) {
		cmd := "am start --user " + user
		if withDisplay {
			cmd += fmt.Sprintf(" --display %d", display)
		}
		out, err := d.client.Shell(ctx, d.info.ID, cmd+" "+args+" 2>&1")
		return string(out), wrapErr(err)
	}
	out, err := start(true)
	if err == nil && display == 0 && strings.Contains(out, "Unknown option: --display") {
		out, err = start(false) // Android 7 and earlier
	}
	if err != nil {
		return err
	}
	if msg, ok := amStartError(out); ok {
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "unable to resolve") {
			return fmt.Errorf("android: %s: %s: %w", what, msg, driver.ErrNotFound)
		}
		return fmt.Errorf("android: %s: %s", what, msg)
	}
	return nil
}

// LinkApp implements driver.LinkOpener.
func (b *Background) LinkApp(ctx context.Context, link string) (driver.App, error) {
	return b.d.LinkApp(ctx, link)
}

// OpenLink implements driver.LinkOpener: the link opens on the virtual
// display, as an app started there would.
func (b *Background) OpenLink(ctx context.Context, link string, app driver.App) error {
	t, err := b.d.linkTarget(ctx, app)
	if err != nil {
		return err
	}
	if err := b.ready(ctx); err != nil {
		return err
	}
	return b.startWith(ctx, t, func() error { return b.d.openLink(ctx, link, t, b.display) })
}

var (
	_ driver.LinkOpener = (*Device)(nil)
	_ driver.LinkOpener = (*Background)(nil)
)
