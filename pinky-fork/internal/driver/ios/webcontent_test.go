package ios

import (
	"testing"
	"time"
)

func TestIsWebContent(t *testing.T) {
	for exe, want := range map[string]bool{
		"/private/preboot/Cryptexes/OS/System/Library/ExtensionKit/Extensions/WebContentExtension.appex/com.apple.WebKit.WebContent":                                  true,
		"/private/preboot/Cryptexes/OS/System/Library/ExtensionKit/Extensions/WebContentEnhancedSecurityExtension.appex/com.apple.WebKit.WebContent.EnhancedSecurity": true,
		"/private/var/containers/Bundle/Application/X/MobileSafari.app/MobileSafari":                                                                                  false,
		"/System/Library/PrivateFrameworks/WidgetKit.framework/XPCServices/com.apple.chronod":                                                                         false,
		"": false,
	} {
		if got := isWebContent(exe); got != want {
			t.Errorf("isWebContent(%q) = %v", exe, got)
		}
	}
}

// A process id given to another process, once the first one is gone, does
// not keep the first one's classification.
func TestWebCacheFollowsProcessIDs(t *testing.T) {
	const web = "/System/Library/ExtensionKit/Extensions/WebContentExtension.appex/com.apple.WebKit.WebContent"
	const widget = "/System/Library/ExtensionKit/Extensions/Widget.appex/Widget"
	t0 := time.Now()
	var c webCache

	if got := c.stale([]int{7, 8, 7}, t0); len(got) != 2 {
		t.Fatalf("stale before any learning: %v", got)
	}
	c.learn([]int{7, 8}, map[uint64]string{7: web, 8: widget}, t0)
	if !c.m[7].web || c.m[8].web {
		t.Fatalf("learned %+v", c.m)
	}
	if got := c.stale([]int{7, 8}, t0.Add(webTTL-time.Second)); len(got) != 0 {
		t.Fatalf("stale within the lifetime: %v", got)
	}

	// Later 7 is a widget and 8 is a page; 9 is not listed at all.
	later := t0.Add(webTTL + time.Second)
	stale := c.stale([]int{7, 8, 9}, later)
	if len(stale) != 3 {
		t.Fatalf("stale after the lifetime: %v", stale)
	}
	c.learn(stale, map[uint64]string{7: widget, 8: web}, later)
	if c.m[7].web || !c.m[8].web {
		t.Errorf("after the processes changed: %+v", c.m)
	}
	if _, ok := c.m[9]; ok {
		t.Errorf("an unlisted process is classified: %+v", c.m)
	}
}

// A classification that expires while the process list is being read is
// made again from that list, not dropped.
func TestWebCacheKeepsWhatExpiresDuringTheRead(t *testing.T) {
	const web = "/System/Library/ExtensionKit/Extensions/WebContentExtension.appex/com.apple.WebKit.WebContent"
	t0 := time.Now()
	var c webCache
	c.learn([]int{7}, map[uint64]string{7: web}, t0)

	pids := []int{7, 8}
	if got := c.stale(pids, t0.Add(webTTL-time.Second)); len(got) != 1 || got[0] != 8 {
		t.Fatalf("stale = %v", got)
	}
	c.learn(pids, map[uint64]string{7: web, 8: "/usr/libexec/keyboard"}, t0.Add(webTTL+time.Second))
	if !c.m[7].web || c.m[8].web {
		t.Errorf("after the read: %+v", c.m)
	}
}
