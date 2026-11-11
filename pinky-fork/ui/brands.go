package ui

import (
	"strings"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
)

// Android phones by maker: where each one keeps Developer options and the
// switches it needs on top of USB debugging. Phones whose settings read
// alike share one entry (OPPO, OnePlus and realme run ColorOS). The menu
// names are the makers' own; locales holds them in each language, and
// where they could not be confirmed the steps say to go by the phone's
// own names. No screenshots: the paths are drawn as menu chips.

// brand is one entry of the Android steps.
type brand struct {
	id   string // "xiaomi", ... "other": driver.Checkup's Family, "other" for ""
	name func() string
	// about is the path to the screen with the item to tap 7 times, and
	// tap that item; aboutNote says what differs on other versions.
	about, tap, aboutNote func() string
	// dev is the path to Developer options once it is on.
	dev func() string
	// switches are what to turn on (or off) there, USB debugging first.
	switches func() []brandSwitch
	// note is anything else the maker's phones need.
	note func() string
	// promptAlways and promptAllow are the check box and the button of
	// the phone's "Allow USB debugging?" prompt, where the maker words
	// them its own way; nil for Android's.
	promptAlways, promptAllow func() string
}

// brandSwitch is a switch to set in Developer options, or elsewhere when
// path says where.
type brandSwitch struct {
	name, why string
	path      string // "" for Developer options
	off       bool   // turn it off rather than on
}

func none() string { return "" }

// brands lists the entries in the order of the picker.
func brands() []brand {
	usb := brandSwitch{name: locales.GuideSwitchUSBDebugging()}
	return []brand{
		{id: "xiaomi", name: locales.BrandXiaomi, about: locales.GuideXiaomiAbout, tap: locales.GuideXiaomiTap,
			aboutNote: locales.GuideXiaomiAboutNote, dev: locales.GuideXiaomiDev, note: locales.GuideXiaomiNote,
			switches: func() []brandSwitch {
				return []brandSwitch{
					{name: locales.GuideXiaomiSwitchUSB()},
					{name: locales.GuideXiaomiSwitchSecurity(), why: locales.GuideXiaomiSwitchSecurityWhy()},
					{name: locales.GuideXiaomiSwitchInstall(), why: locales.GuideXiaomiSwitchInstallWhy()},
				}
			}},
		{id: "oppo", name: locales.BrandOppo, about: locales.GuideOppoAbout, tap: locales.GuideOppoTap,
			aboutNote: locales.GuideOppoAboutNote, dev: locales.GuideOppoDev, note: locales.GuideOppoNote,
			switches: func() []brandSwitch {
				return []brandSwitch{usb, {name: locales.GuideOppoSwitchMonitor(), why: locales.GuideOppoSwitchMonitorWhy()}}
			}},
		{id: "vivo", name: locales.BrandVivo, about: locales.GuideVivoAbout, tap: locales.GuideVivoTap,
			aboutNote: locales.GuideVivoAboutNote, dev: locales.GuideVivoDev, note: locales.GuideVivoNote,
			switches: func() []brandSwitch { return []brandSwitch{usb} }},
		{id: "honor", name: locales.BrandHonor, about: locales.GuideHonorAbout, tap: locales.GuideHonorTap,
			aboutNote: locales.GuideHonorAboutNote, dev: locales.GuideHonorDev, note: locales.GuideHonorNote,
			promptAlways: locales.GuideHonorPromptAlways, promptAllow: locales.GuidePromptOK,
			switches: func() []brandSwitch {
				// The charge-only switch first: without it, HONOR turns USB
				// debugging off again after a while.
				return []brandSwitch{{name: locales.GuideHonorSwitchCharge(), why: locales.GuideHonorSwitchChargeWhy()}, usb}
			}},
		{id: "huawei", name: locales.BrandHuawei, about: locales.GuideHuaweiAbout, tap: locales.GuideHuaweiTap,
			aboutNote: locales.GuideHuaweiAboutNote, dev: locales.GuideHuaweiDev, note: locales.GuideHuaweiNote,
			promptAlways: locales.GuideHuaweiPromptAlways, promptAllow: locales.GuidePromptOK,
			switches: func() []brandSwitch {
				return []brandSwitch{
					{name: locales.GuideHuaweiSwitchCharge(), why: locales.GuideHuaweiSwitchChargeWhy()},
					usb,
					{name: locales.GuideHuaweiSwitchMonitor(), why: locales.GuideHuaweiSwitchMonitorWhy(), off: true},
				}
			}},
		{id: "samsung", name: locales.BrandSamsung, about: locales.GuideSamsungAbout, tap: locales.GuideSamsungTap,
			aboutNote: none, dev: locales.GuideSamsungDev, note: locales.GuideSamsungNote,
			switches: func() []brandSwitch {
				// Auto Blocker first: while it is on, USB debugging stays off.
				return []brandSwitch{{name: locales.GuideSamsungAutoBlocker(), why: locales.GuideSamsungAutoBlockerWhy(),
					path: locales.GuideSamsungAutoBlockerPath(), off: true}, usb}
			}},
		{id: "pixel", name: locales.BrandPixel, about: locales.GuidePixelAbout, tap: locales.GuidePixelTap,
			aboutNote: locales.GuidePixelAboutNote, dev: locales.GuidePixelDev, note: none,
			switches: func() []brandSwitch { return []brandSwitch{usb} }},
		{id: "other", name: locales.BrandOther, about: locales.GuideOtherAbout, tap: locales.GuideOtherTap,
			aboutNote: locales.GuideOtherAboutNote, dev: locales.GuideOtherDev, note: locales.GuideOtherNote,
			switches: func() []brandSwitch { return []brandSwitch{usb} }},
	}
}

// brandByID returns the entry with the id, or the stock Android one.
func brandByID(id string) brand {
	all := brands()
	for _, b := range all {
		if b.id == id {
			return b
		}
	}
	return all[len(all)-1]
}

// brandOfFamily is the entry for a driver.Checkup's Family.
func brandOfFamily(family string) string {
	switch family {
	case "xiaomi", "oppo", "vivo", "honor", "huawei", "samsung", "pixel":
		return family
	}
	return "other"
}

// pathParts splits a menu path written "Settings › About phone".
func pathParts(path string) []string {
	var out []string
	for _, p := range strings.Split(path, "›") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
