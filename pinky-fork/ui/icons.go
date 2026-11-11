package ui

import (
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/assets"
)

// icon makes a line icon from SVG path data, drawn as the prototype's
// .ico: 24-unit box, 1.75 stroke, round caps and joins.
func icon(body string) *mg.SVG {
	return mg.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

// filledIcon makes an icon of filled shapes.
func filledIcon(body string) *mg.SVG {
	return mg.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" fill="currentColor">` + body + `</svg>`))
}

var (
	icGrid     = icon(`<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/>`)
	icPlus     = icon(`<path d="M12 5v14"/><path d="M5 12h14"/>`)
	icHistory  = icon(`<path d="M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8"/><path d="M3 3v5h5"/><path d="M12 7v5l4 2"/>`)
	icSettings = icon(`<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>`)
	icHand     = icon(`<path d="M18 11V6a2 2 0 0 0-4 0v5"/><path d="M14 10V4a2 2 0 0 0-4 0v6"/><path d="M10 10.5V6a2 2 0 0 0-4 0v8"/><path d="M18 8a2 2 0 1 1 4 0v6a8 8 0 0 1-8 8h-2c-2.8 0-4.5-.86-5.99-2.34l-3.6-3.6a2 2 0 0 1 2.83-2.82L7 15"/>`)
	icBack     = icon(`<path d="M17 5 7 12l10 7z"/>`)
	icHome     = icon(`<circle cx="12" cy="12" r="7"/>`)
	icRecents  = icon(`<rect x="6" y="6" width="12" height="12" rx="2"/>`)
	icMore     = filledIcon(`<circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/>`)
	icCheck    = icon(`<path d="M20 6 9 17l-5-5"/>`)
	icPerson   = icon(`<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>`)
	icTouchID  = icon(`<path d="M12 11v3c0 2-.5 3.6-1.4 5"/><path d="M8.5 7.6A5 5 0 0 1 17 11v1.5"/><path d="M7 11.3a5 5 0 0 1 .4-2"/><path d="M7 14.5c0 1.4-.3 2.6-.9 3.6"/><path d="M17 15.5c0 1.5-.3 2.9-.8 4"/><path d="M14.8 13.5c0 2.6-.6 4.7-1.7 6.5"/><path d="M4.6 9.2a8.5 8.5 0 0 1 15.2-1.4"/>`)
	icLock     = icon(`<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>`)
	icWifi     = icon(`<path d="M12 20h.01"/><path d="M2 8.82a15 15 0 0 1 20 0"/><path d="M5 12.86a10 10 0 0 1 14 0"/><path d="M8.5 16.43a5 5 0 0 1 7 0"/>`)
	icWifiOff  = icon(`<path d="M2 2l20 20"/><path d="M8.5 16a5 5 0 0 1 7 0"/><path d="M5 12.5a10 10 0 0 1 5.17-2.78"/><path d="M19 12.5a10 10 0 0 0-2.4-1.73"/><path d="M12 19.5h.01"/>`)
	icSearch   = icon(`<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>`)
	icPlay     = icon(`<path d="M7 4l13 8-13 8z"/>`)
	icPause    = icon(`<rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/>`)
	icCopy     = icon(`<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`)
	icExport   = icon(`<path d="M12 3v12"/><path d="m7 8 5-5 5 5"/><path d="M5 15v4a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-4"/>`)
	icX        = icon(`<path d="M18 6 6 18"/><path d="m6 6 12 12"/>`)
	icChevL    = icon(`<path d="m15 18-6-6 6-6"/>`)
	icChevR    = icon(`<path d="m9 18 6-6-6-6"/>`)
	icChevU    = icon(`<path d="m18 15-6-6-6 6"/>`)
	icChevD    = icon(`<path d="m6 9 6 6 6-6"/>`)
	icAndroid  = icon(`<rect x="6" y="2" width="12" height="20" rx="2.5"/><path d="M11 18h2"/>`)
	icIphone   = icon(`<rect x="6" y="2" width="12" height="20" rx="3.5"/><path d="M10 5h4"/>`)
	icAlert    = icon(`<path d="M12 9v4"/><path d="M12 17h.01"/><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/>`)
	icExpand   = icon(`<path d="M15 3h6v6"/><path d="M9 21H3v-6"/><path d="m21 3-7 7"/><path d="m3 21 7-7"/>`)
	icEyeOff   = icon(`<path d="M10.7 5.1A10.4 10.4 0 0 1 12 5c7 0 10 7 10 7a13.2 13.2 0 0 1-1.7 2.7"/><path d="M14.1 14.2a3 3 0 1 1-4.2-4.2"/><path d="M17.5 17.5A10.4 10.4 0 0 1 12 19c-7 0-10-7-10-7a13.2 13.2 0 0 1 4.6-5.3"/><path d="m2 2 20 20"/>`)
	icCamera   = icon(`<path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3l-2.5-3z"/><circle cx="12" cy="13" r="3"/>`)
	icMonitor  = icon(`<rect x="2" y="3" width="20" height="14" rx="2"/><rect x="9.5" y="6" width="5" height="8" rx="1"/><path d="M8 21h8"/><path d="M12 17v4"/>`)
	icStop     = icon(`<circle cx="12" cy="12" r="9"/><rect x="9" y="9" width="6" height="6" rx="1"/>`)
	icShield   = icon(`<path d="M12 21.5s7.5-3.6 7.5-9.5V5.5L12 2.5 4.5 5.5V12c0 5.9 7.5 9.5 7.5 9.5z"/>`)
	icSliders  = icon(`<path d="M4 21v-7"/><path d="M4 10V3"/><path d="M12 21v-9"/><path d="M12 8V3"/><path d="M20 21v-5"/><path d="M20 12V3"/><path d="M2 14h4"/><path d="M10 8h4"/><path d="M18 16h4"/>`)
	icInbox    = icon(`<path d="M22 12h-6l-2 3h-4l-2-3H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/>`)
	icPlug     = icon(`<path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8z"/>`)

	logo = mg.MustParseSVG(assets.Logo)
)
