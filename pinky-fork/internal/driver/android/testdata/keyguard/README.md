# Lock screen dumps

Fixtures for the Android driver's PIN pad tests (`pinpad_test.go`). The file name says what each one is:

- `real-*`: taken from our emulators with `uiautomator dump` (or the companion's tree, `.companion.json`), nothing edited: the lock screen after waking (`-lockscreen`) and AOSP's PIN pad after `wm dismiss-keyguard` (`-pin`), on Android 10, 16 and 17.
- `synthetic-*`: made up from what is published about each OEM lock screen, not taken from a phone. The comment at the top of each file says what it is based on (with sources) and what is invented. They check that the driver reads those layouts; they do not show that those phones accept the taps.

Never take dumps of someone's phone without them: the lock screen shows their notifications. A new real dump of a phone needs the person's go-ahead, and its notifications removed before it is committed.
