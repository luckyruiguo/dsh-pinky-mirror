package gateway

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// Finding an app by the name an agent gives. Only a sure match is taken: the
// id, the app's name or one of its aliases (an iPhone's home screen name,
// driver.App.Aliases), or a well-known name below. A name that is only part
// of an app's name is not: "微信" is part of "微信读书" (WeRead), while WeChat's
// bundle is named WeChat on an iPhone in any language (GitHub issue #7). Such
// apps are offered to the agent to choose from instead.

// appMatch is what a name finds among the installed apps.
type appMatch struct {
	app driver.App
	ok  bool
	// similar are apps the name may mean: names that hold it, or several
	// apps of that name. The agent names the one it means.
	similar []driver.App
}

// knownApps are names people use for well-known apps whose installed name
// may differ, such as an iPhone app's English bundle name (WeChat for 微信),
// by the ids of their iOS and Android builds. A name finds the app only
// when one of these ids is installed.
var knownApps = map[string][]string{
	"微信":     {"com.tencent.xin", "com.tencent.mm"},
	"wechat": {"com.tencent.xin", "com.tencent.mm"},
	"企业微信":   {"com.tencent.ww", "com.tencent.wework"},
	"wecom":  {"com.tencent.ww", "com.tencent.wework"},
	"微信读书":   {"com.tencent.weread"},
	"weread": {"com.tencent.weread"},
	"支付宝":    {"com.alipay.iphoneclient", "com.eg.android.AlipayGphone"},
	"alipay": {"com.alipay.iphoneclient", "com.eg.android.AlipayGphone"},
	"淘宝":     {"com.taobao.taobao4iphone", "com.taobao.taobao"},
	"taobao": {"com.taobao.taobao4iphone", "com.taobao.taobao"},
	"抖音":     {"com.ss.iphone.ugc.Aweme", "com.ss.android.ugc.aweme"},
	"douyin": {"com.ss.iphone.ugc.Aweme", "com.ss.android.ugc.aweme"},
	"微博":     {"com.sina.weibo"},
	"weibo":  {"com.sina.weibo"},
	"网易云音乐":  {"com.netease.cloudmusic"},
}

// matchApp finds an app by a sure match (see findApp).
func matchApp(apps []driver.App, name string) (driver.App, bool) {
	m := findApp(apps, name)
	return m.app, m.ok
}

// findApp finds an app by id, then by its name or an alias, then by a
// well-known name. Failing those, apps whose name or alias holds the name
// are only similar. With no name to go on at all, as an English name on a
// phone in another language ("Calculator" for 计算器), the parts of the
// package or bundle ids are tried (com.google.android.calculator).
func findApp(apps []driver.App, name string) appMatch {
	name = strings.TrimSpace(name)
	for _, a := range apps {
		if a.ID == name {
			return appMatch{app: a, ok: true}
		}
	}
	q := strings.ToLower(name)
	if q == "" {
		return appMatch{}
	}
	var exact, part []driver.App
	for _, a := range apps {
		switch {
		case appNamed(a, q, func(n string) bool { return n == q }):
			exact = append(exact, a)
		case appNamed(a, q, func(n string) bool { return strings.Contains(n, q) }):
			part = append(part, a)
		}
	}
	if len(exact) > 0 {
		if a, ok := pickApp(exact); ok {
			return appMatch{app: a, ok: true}
		}
		return appMatch{similar: exact}
	}
	if ids := knownApps[q]; len(ids) > 0 {
		var known []driver.App
		for _, a := range apps {
			if slices.Contains(ids, a.ID) {
				known = append(known, a)
			}
		}
		if len(known) == 1 {
			return appMatch{app: known[0], ok: true}
		}
	}
	if len(part) > 0 {
		return appMatch{similar: part}
	}
	byID := idMatches(apps, q)
	if a, ok := pickApp(byID); ok {
		return appMatch{app: a, ok: true}
	}
	return appMatch{similar: byID}
}

// appNamed reports whether an app's name or one of its aliases, in lower
// case, passes is.
func appNamed(a driver.App, q string, is func(string) bool) bool {
	if is(strings.ToLower(a.Name)) {
		return true
	}
	for _, al := range a.Aliases {
		if is(strings.ToLower(al)) {
			return true
		}
	}
	return false
}

// pickApp returns the only app of a list, or else its only user app.
func pickApp(list []driver.App) (driver.App, bool) {
	if len(list) == 1 {
		return list[0], true
	}
	var user []driver.App
	for _, a := range list {
		if !a.System {
			user = append(user, a)
		}
	}
	if len(user) == 1 {
		return user[0], true
	}
	return driver.App{}, false
}

// idMatches returns the apps whose id has a part equal to q, or else a
// part that contains it ("clock" in com.google.android.deskclock). q is
// lower case; parts shorter than four letters only match whole.
func idMatches(apps []driver.App, q string) []driver.App {
	q = strings.ReplaceAll(q, " ", "")
	if len(q) < 3 {
		return nil
	}
	var whole, part []driver.App
	for _, a := range apps {
		matched := false
		for i, seg := range strings.Split(strings.ToLower(a.ID), ".") {
			if i == 0 && (seg == "com" || seg == "org" || seg == "net" || seg == "app") {
				continue
			}
			switch {
			case seg == q:
				whole = append(whole, a)
				matched = true
			case len(q) >= 4 && strings.Contains(seg, q) && !matched:
				part = append(part, a)
				matched = true
			}
			if matched {
				break
			}
		}
	}
	if len(whole) > 0 {
		return whole
	}
	return part
}

// appList names apps for the agent: "微信读书 (com.tencent.weread)", at most
// five.
func appList(apps []driver.App) string {
	var parts []string
	for i, a := range apps {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(apps)-5))
			break
		}
		name := a.Name
		if len(a.Aliases) > 0 {
			name += " · " + strings.Join(a.Aliases, " · ")
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, a.ID))
	}
	return strings.Join(parts, ", ")
}
