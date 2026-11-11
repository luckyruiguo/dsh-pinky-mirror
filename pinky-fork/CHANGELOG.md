# Changelog

What changed for people in each release of Pinky, newest first. A version is the release's date in Asia/Shanghai and that day's count: 2026.1107.2 is the second release of 7 November 2026. Notes wait under "Unreleased" until the next release takes them; see [docs/RELEASING.md](docs/RELEASING.md).

## 2026.1111.13 (2026-11-11)

- On a Mac with Xcode, Pinky can now install its helper on an older iPhone from its window (the phone's page, its checkup, and Add phone › Try an older iPhone), and install it again when its signature is about to run out, showing each step, asking you to unlock the phone when needed, and saying why when it fails.
- An iPhone on iOS 27 that Pinky can't show because an app on it is using the camera or microphone (during a call, for example) now says so on its page and in the sidebar, with what to do, and connects by itself once they're free.
- Agents can no longer uninstall or stop the Pinky helper on an iPhone, whatever its bundle ID.

## 2026.1111.12 (2026-11-11)

- You can now turn a phone's screen yourself: Rotate in the ⋯ menu under its picture offers portrait, landscape and the other ways it turns, with the current one marked. On an iPhone or iPad it needs the iPhone helper. Thanks @NaNhkNaN.
- On iOS 27, when the iPhone helper is installed, Pinky moves the phone to it by itself for what only the helper can do, such as turning the screen or a pinch, also an agent's, and goes back to the quicker connection afterwards.
- Pinching on the picture of an iPhone or iPad on iOS 27 now works when the iPhone helper is installed. The first pinch moves the phone to the helper; the next one goes through.

## 2026.1111.11 (2026-11-11)

- The iPhone helper (WebDriverAgent) now shows on the phone as Pinky Helper, with Pinky's icon.
- On a Mac with Xcode, `pinkyctl helper install` builds, signs and installs the iPhone helper in one command, also on a phone that is only on Wi-Fi. Run it again when the helper's signature runs out.

## 2026.1111.10 (2026-11-11)

- Pinky has a new, calmer look: a translucent sidebar on macOS and Windows 11, one set of sizes and spacing across every screen, and Settings in grouped rows.
- Activity shows a task's steps on a timeline, each with its picture.
- Add phone's choices, the dialogs, and the phone page's empty and error states are tidier and consistent.

## 2026.1111.9 (2026-11-11)

- Ask Pinky: type a task at the top of Tasks and Pinky does it on the phone by itself with an AI model you choose (an Anthropic- or OpenAI-compatible API, with your own key kept in the system keychain), under your rules and approvals and within step, time and cost limits.

## 2026.1111.8 (2026-11-11)

- When one agent app runs several conversations at once, as Claude Cowork does, one conversation can no longer take over or tap on a phone that another conversation is using.
- An agent that starts a new task on a phone still running its previous task is now told to end that task first, instead of Pinky ending it for the new one.

## 2026.1111.7 (2026-11-11)

- The panel that opens from a phone's battery now widens to fit its lines, so the battery and storage no longer break onto two lines in Chinese and Japanese.

## 2026.1111.6 (2026-11-11)

- A phone's page now shows its battery at the top right. Click it to see the phone's model, system version, battery and free storage. All phones shows each phone's battery too.
- Agents that read a phone's settings now also get its model, system, battery and storage.

## 2026.1111.5 (2026-11-11)

- Pinky no longer quits suddenly on Intel Macs while it shows a phone's screen. Thanks @chenjunpu.

## 2026.1111.4 (2026-11-11)

- A remote agent whose client opens a new session for every call no longer loses its task two minutes after its first call: the wait starts again each time a session closes. Thanks @pseudoyu.
- Stop all no longer shows before any phone is added.

## 2026.1111.3 (2026-11-11)

- Leaving Pinky on All phones no longer keeps an idle iPhone from locking: its tile shows the last picture until a task runs on it, so screen sharing stops and the phone locks by itself.

## 2026.1111.2 (2026-11-11)

- You can now pause Pinky's connection to a phone without removing it: Pause connection in the phone's menu stops its screen stream and keeps agents off it until you resume it or plug it in again, so the camera, microphone and banking apps work as usual.
- The phone page now shows when Pinky is streaming the phone's screen.

## 2026.1111.1 (2026-11-11)

- Agents in Claude Code now see the shape of a batch's steps and of the apps list, so they no longer have to guess it.
- Handing an iPhone on Wi-Fi to you to unlock no longer fails when the phone drops off the network for a moment as it unlocks.
- A locked iPad on Wi-Fi no longer fills the log with lines about it coming and going.

## 2026.1110.32 (2026-11-10)

- On Windows, the live picture of iPhones on iOS versions before 27 now works. Thanks to @kingmissh.

## 2026.1110.31 (2026-11-10)

- Pinky now makes sure the WebDriverAgent helper ends on the iPhone after a task, after a crash and when Pinky quits, so the phone locks on its own again.

## 2026.1110.30 (2026-11-10)

- Phone actions now answer within 45 seconds: a slow step comes back as uncertain for the agent to check, instead of the agent's app timing out and dropping the task.
- When a local agent's connection to Pinky restarts, its new connection carries on with the same task, keeping the apps you allowed.
- Reading an iPhone's screen through the WebDriverAgent helper over Wi-Fi is faster, and web pages inside apps are read once their content has loaded.
- When an iPhone app does not take pasted text (for example behind iOS's Allow Paste prompt), typing now reports that it failed and does not press Enter. Thanks to @stoicneko for the report.
- In read-only mode, agents can type in search boxes that apps draw themselves, such as Xianyu's.

## 2026.1110.29 (2026-11-10)

- Opening China Merchants Bank on an iPhone by its app id now moves the task to the WebDriverAgent helper, as opening it by name already did.
- When the iPhone asks for its passcode to allow the WebDriverAgent helper, Pinky now tells you to enter it on the phone and waits for it, instead of quietly staying on CoreDevice.
- Agents can no longer tap the keys of an app's own passcode keypad that is hidden from screenshots; they hand the phone to you instead, so a stray tap cannot use up a passcode attempt.
- An agent whose connection to Pinky was opened before Pinky updated is now told its tools changed, so new options are not silently ignored.

## 2026.1110.28 (2026-11-10)

- Scrolling the phone mirror with a mouse wheel now glides smoothly and flings like a swipe, instead of jumping a notch at a time. Thanks to @stoicneko for the report.

## 2026.1110.27 (2026-11-10)

- Removing one of two blocked apps with the same name (say Trust Bank on an iPhone and Trust Wallet on an Android phone) no longer makes Pinky quit, and unblocks only that app.
- Blocked apps now lists the apps of an iPhone on Wi-Fi once it comes online, so an app agents are kept out of no longer goes missing from the list.

## 2026.1110.26 (2026-11-10)

- Pinky started from Finder now reads Tailscale correctly on macOS, instead of failing with "The Tailscale GUI failed to start". Thanks to @lihongjie224.

## 2026.1110.25 (2026-11-10)

- The guide Pinky installs into your agents (SKILL.md) now updates with Pinky: each time Pinky starts, it replaces an older copy with the current one, so you no longer need to connect the agent again.

## 2026.1110.24 (2026-11-10)

- When Pinky Companion does not come on, the reason Pinky shows no longer includes the names of your other accessibility apps, so it is safe to paste into a public report.

## 2026.1110.23 (2026-11-10)

- On an iPhone, the on-screen keyboard now comes back after an agent types, for you and for apps that wait for it. When typed text went nowhere because no field had focus, the agent is told so and tries again instead of carrying on.
- Installing an app from the computer now offers Always allow. It covers one agent on one phone, for builds of the same app signed by the same developer, so your next builds go in without asking. Another app or another signer still asks, and uninstalling asks every time. Take it back in Settings › Safety.

## 2026.1110.22 (2026-11-10)

- On Linux, when Pinky started by an agent cannot open its window (no display, or missing libraries), Pinky now keeps running in the background as pinky daemon, so the agent stays connected. Thanks to pseudoyu.

## 2026.1110.21 (2026-11-10)

- Swipes, taps, drags and pinches on an element now stay clear of the on-screen keyboard instead of landing on its keys. When too little of the element shows above the keyboard, the agent is told to hide the keyboard first.
- An iPhone left on Automatic with the WebDriverAgent helper installed now switches a task to the helper by itself when the task opens DBS digibank or China Merchants Bank, so those apps run, and goes back to CoreDevice after the task. Agents can also ask for the helper with connection in phone_connect.

## 2026.1110.20 (2026-11-10)

- Quitting Pinky now also stops the adb it brought for Android phones, so no adb process is left running, unless another app such as Android Studio is using it. An adb you installed yourself keeps running. Turn this off in Settings › Advanced.

## 2026.1110.19 (2026-11-10)

- Unlocking a vivo phone from the computer now works when its lock screen asks for a fingerprint first: Pinky taps "Use password" and enters the PIN.
- Checking for and downloading updates now goes through GitHub's API when GitHub's download links fail, as they did for hours on 8 November.
- When Pinky Companion's service does not start on a phone, the message now also says what Android reported about the app's process, and Help › Send Feedback… includes it.
- On the very first run, an agent that connected the moment Pinky started could be refused with "bad agent token". It now connects.
- On iPhone, tapping a switch in Settings or a SwiftUI app now flips it instead of tapping its label, and the agent is told when a switch stayed as it was.

## 2026.1110.18 (2026-11-10)

- Dragging the timeline of a recorded task jumps straight to that moment and shows the right picture, also while paused, instead of replaying from the start. Thanks to tarou-jp.

## 2026.1110.17 (2026-11-10)

- On Windows, agents in the Claude and Codex desktop apps from the Microsoft Store now reach the Pinky you have open, instead of quietly starting a second one that Stop all could not reach. Thanks to stoicneko.
- On Windows, opening Pinky while an agent had already started it in the background no longer quits without a word: Pinky takes over, and the agent reconnects by itself. Thanks to stoicneko.

## 2026.1110.16 (2026-11-10)

- Approvals to change a phone's settings (rotation, appearance, location, language) and to read an app's logs now offer **More options › Always allow**, remembered for that agent and phone (and app, for logs) until you take it back in Settings › Safety. Thanks to vinsew.

## 2026.1110.15 (2026-11-10)

- When a phone is too slow to answer, agents are now told it timed out and what to do next, instead of that the call was cancelled. Thanks to NaNhkNaN.

## 2026.1110.14 (2026-11-10)

- On Linux, Pinky no longer leaves a new folder behind in your runtime folder each time it starts with phones over Wi-Fi on, and removes the ones earlier versions left. Thanks to pseudoyu.

## 2026.1110.13 (2026-11-10)

- On Windows, confirming with the password now works for an account without a password. When Windows still refuses to check it, Pinky says to set a password or a Windows Hello PIN.

## 2026.1110.12 (2026-11-10)

- An iPhone driven through the WebDriverAgent helper now reconnects by itself within seconds when the helper stops, instead of failing every action until Pinky is restarted. Thanks to NaNhkNaN.

## 2026.1110.11 (2026-11-10)

- On Windows, an iPhone that trusts this computer but still can't connect now says why, with a command to fix it that you can copy, and Pinky asks the iPhone to trust the computer again when the phone rejects its pairing record, instead of waiting for a tap that can't help. Thanks to kingmissh.

## 2026.1110.10 (2026-11-10)

- Agents can now tidy an Android home screen even when you have them work in the background: they can ask to use the phone's own screen for it, and you stay in control while you use the phone.
- Dragging an app on a Pixel home screen now lets go where the target went, although the launcher shrinks the page as soon as an app is picked up.
- While Pinky shows the phone, a drag, swipe or pinch that waits for you now draws its route on the phone's picture.
- When Pinky Companion is installed but its service does not come on, Pinky now says why, instead of saying the install did not finish. Thanks to pigracing.
- A phone that refuses installs over USB, such as a Xiaomi without a SIM card, now gets the Pinky Companion APK and steps to install it by hand, and Pinky no longer retries a refused update every few hours. Thanks to kunl.
- Agents on your tailnet, such as Grok Bot, can now reach a Pinky that serves remote agents through tailscale serve, instead of getting an error 403.

## 2026.1110.9 (2026-11-10)

- On a Mac, phones on Wi-Fi now come back by themselves after Pinky updates: Pinky restarts the adb server it started when that server lost Local Network access, and the checkup says what to do when another program's adb server can't reach your network. Thanks to NaNhkNaN.

## 2026.1110.8 (2026-11-10)

- Agents can now wait for whichever of several texts shows first, and only in one part of the screen, so they no longer keep pausing and looking again when they cannot tell what comes next. Thanks to chenzl25.

## 2026.1110.7 (2026-11-10)

- iPhones that Pinky drives through the WebDriverAgent helper now connect over Wi-Fi without a cable or Xcode, instead of failing with "WebDriverAgent did not start". Thanks to NaNhkNaN.

## 2026.1110.6 (2026-11-10)

- Pinky no longer refuses to type into a text field just because its hidden name contains a word like "footprint" or "barcode", which likely kept agents from typing into a note in Apple Notes on iOS 27. Thanks to KenWuqianghao.

## 2026.1110.5 (2026-11-10)

- Agents can now drag: move app icons to another home screen page or into a folder, reorder lists and move sliders, with the same approvals and rules as a swipe. Thanks to issenzhang.

## 2026.1110.4 (2026-11-10)

- Confirming it's you on Windows with an account that has no password now also works on the computers the previous fix missed, such as ones with long names: leave the password empty. Thanks to Kutius.
- On Windows, if your account is a Microsoft account you sign in to with a PIN or Windows Hello, Pinky now tells you to type your Microsoft account password when confirming it's you, instead of just failing.

## 2026.1110.3 (2026-11-10)

- On Linux, logging out and back in no longer lets a second Pinky start on the same history and phones while the first one is still running. Thanks to pseudoyu.

## 2026.1110.2 (2026-11-10)

- Confirming it's you on Windows now works when your Windows account has no password: leave the password empty and confirm. Thanks to Kutius.

## 2026.1110.1 (2026-11-10)

- On Linux, a Pinky started by a service or another program that leaves out `XDG_RUNTIME_DIR` now finds the Pinky already running on your desktop, instead of starting a second one whose approval requests never reach the window. Thanks to pseudoyu.

## 2026.1109.24 (2026-11-09)

- When a new agent waits for you to approve it, asking again now keeps waiting for your answer, instead of failing and starting a second approval.
- Force-quitting Pinky no longer leaves the iOS Simulator's WebDriverAgent build running in the background.
- With `PINKY_DEVICES` set, Pinky no longer queries or reconnects to the Android phones and emulators it leaves out.

## 2026.1109.23 (2026-11-09)

- Adding an iPhone on a Windows computer no longer fails with "certificate signed by unknown authority" when Windows has not downloaded Apple's root certificate.

## 2026.1109.22 (2026-11-09)

- `phone_observe` now tells agents that their app may show them a smaller copy of a screenshot, and that `save` keeps it at full resolution. Thanks to ferrraridave-coder.
- Stopping an app on an iPhone from a Windows computer now works.
- When an agent saves a task's report just as the screen recording starts, Pinky now still asks whether to give it the recording.

## 2026.1109.21 (2026-11-09)

- Agents can now turn the screen, switch between light and dark appearance, simulate a location and open an app in another language to test it. On a real phone you approve these first, and Pinky puts everything back when the task ends.

## 2026.1109.20 (2026-11-09)

- The live picture of a phone stops redrawing sooner once the phone's screen goes still, which saves power.

## 2026.1109.19 (2026-11-09)

- Agents can now install, update, uninstall and force-quit apps you build (an APK, or an `.ipa`/`.app` signed for your iPhone) from the phone's folder on the computer. Installing and uninstalling always ask you, with the app's name, version and signer, and **Allow for this task** covers an agent's next builds of the same app.
- Agents on another computer can now upload files for an iPhone, to install apps from.
- Agents can now read the log lines and crash reports of an app they are testing, to see why it failed. The first time in a task Pinky asks you (**Allow for this task** stops it asking again), except for development builds. Agents only get that app's own lines, and never the logs of banking, wallet, password-manager or blocked apps.
- A task's history shows how many log lines or crash reports an agent read, never what they said.
- Agents can now open links with `phone_launch` on Android (also on the background display) and iPhone: web pages, an app's own links (`myapp://…`), and call, message and email links. A link is judged as the app that opens it and as the tap it stands for, so blocked apps stay blocked and a payment link asks like a Pay button; the approval and Activity name the link and the app.
- Links that reach past the app they open in (`file:`, `content:`, `intent:`, `javascript:`, `data:`, Shortcuts) are never opened, under the new rule Unsafe links.
- Agents can pinch to zoom maps, photos and pages in and out, and turn them with two fingers (`phone_pinch`); iPhones need the WebDriverAgent helper for it.
- You can pinch on the phone's picture too: pinch on the trackpad, or drag with Option held (Alt on Windows and Linux) as in Xcode's Simulator.
- Agents can now save a screenshot as a file (`phone_observe` with `save`) and, as a task ends, its report as HTML and JSON (`phone_release` with `export`), for a bug report or a pull request. They go into the phone's folder on your computer and show only what the agent may see; agents on another computer get one-time download links.
- When an agent exports a recorded task, Pinky asks you whether it gets the screen recording too. Recordings show one-time codes and blocked apps' notifications, so they stay on your computer unless you allow it, each time; if you decline, the agent gets the report alone.
- Start an iOS Simulator or an Android emulator from **Add phone › Virtual phone** and use it as a phone, with the live picture, recordings, approvals and rules. Only you can start one.
- An emulator that quits while it starts now says why.
- Emulators Pinky starts have no sound, so they no longer crash while a Bluetooth headset switches to its microphone.
- An Android phone that is still starting up shows as starting, instead of getting a garbled name and no picture.
- On Android, Pinky can now type into text fields that refuse its usual way in, such as WeChat's chat box, including on the background display and with Chinese keyboards.
- On Android, when typed text does not reach the field, or the field ends up reading something else, the agent now gets an error instead of a silent "no change".
- Pinky now makes sure an Android phone it reaches over Wi-Fi is the phone you set up: another device on the network can no longer answer in its place. If you reset a phone yourself, its page offers to trust it again, or plug it in once.
- Android phones connected over Wireless debugging that Pinky hasn't used now wait for you to choose Use before Pinky touches them.
- iPhones with two screens, such as the iPhone Duo, now connect with the screen in use, unfolded or folded, and switch when you fold or unfold while connected.
- When an iPhone sends no live picture, the error now says which screen Pinky asked for, its checkup says what came instead, and Send feedback includes the details.
- Pinky now knows the iPhone 17e, iPhone 18 Pro Max, iPhone Duo and recent iPads by name.

## 2026.1109.18 (2026-11-09)

- In Settings › Agents, removing Pinky from an agent that changes its settings through its own command now shows an error when Pinky cannot read those settings, instead of saying Removed while the agent may still use Pinky. Thanks to tarou-jp.

## 2026.1109.17 (2026-11-09)

- On Linux, a Pinky unpacked from the `.tar.gz` now installs updates by itself: it checks the download, swaps in the new version's files, restarts into it, and goes back to the old version if the new one does not start. Other files in the same folder stay where they are. Thanks to pseudoyu.

## 2026.1109.16 (2026-11-09)

- On Linux and Windows, a paired iPhone or iPad on Wi-Fi now keeps its address while it answers there, so a forged network announcement can no longer pull Pinky's connection elsewhere; addresses no phone can have are ignored.
- On Windows, saving in Settings › Rules no longer fails when another program, such as a virus scanner, a backup or your editor, has the rules file open for a moment.

## 2026.1109.15 (2026-11-09)

- Pinky now checks that an iPhone or iPad it connects to is the one paired with this computer, by the certificate from pairing, so another device on the network can't stand in for it over Wi-Fi.

## 2026.1109.14 (2026-11-09)

- Agents can have a tap, swipe, typing, key press or app launch wait less for the screen to settle (`settle_ms`), so in games and videos, whose screens never stop moving, an action no longer waits 3 s for nothing. Actions you approve still wait the full time. Thanks to chenzl25.
- On an iPhone, Pinky now checks what typing put in the field: text that came out different, for example with a keyboard layout other than U.S., is reported instead of counting as typed. When typing key by key would enter the wrong letters, Pinky says why instead of typing them. Typing also no longer drops the first space after switching away from a Chinese keyboard, or waits twice for a phone clipboard that does not answer. Thanks to theniceboy.
- On Android, when an app opened on the background display does not show up there yet, Pinky now waits a few seconds and nudges the display. If the screen stays empty, it tells the agent so instead of returning an empty screen, and a screenshot then gives the right reason. Thanks to crown3.
- On Android 17, a task no longer moves to the phone's own screen when the background display cannot be read for a moment right after an app opens there.

## 2026.1109.13 (2026-11-09)

- An Android phone that hides its serial number from other apps is no longer listed once for each of its Wi-Fi connections, which could fill Phones and the menu bar with dozens of copies of the same phone. Pinky also no longer opens a second Wi-Fi connection to a phone that is already connected over Wi-Fi.

## 2026.1109.12 (2026-11-09)

- On Windows, an iPhone now keeps working over Wi-Fi when you unplug its cable, instead of going offline or staying "Locked on Wi-Fi" though it was unlocked. Thanks to yaoqiangyao.
- On Windows, Pinky no longer leaves an adb console window open when the adb on your PATH comes from scoop.

## 2026.1109.11 (2026-11-09)

- An Android phone's picture no longer stays on "Connecting…" when its video encoder stops after a still screen: Pinky restarts the video within a few seconds. Thanks to NaNhkNaN.

## 2026.1109.10 (2026-11-09)

- Agents can now find and open Android app clones (Dual apps, App Twin, Dual Messenger): each clone is listed as an app of its own, such as "WeChat (clone)", and opens on the phone's screen or in the background. A clone of a blocked app is blocked too.
- Pinky needs macOS 13 or later, as it did in practice already: the app, its Homebrew cask and the README now say so, instead of macOS 12.
- Choosing Use on an iPhone or iPad found on Wi-Fi no longer sometimes drops it for a moment and connects it a second time.
- An agent waiting for your approval now gets your answer even when you answer at the very moment its call stops waiting, instead of having to repeat the call.

## 2026.1109.9 (2026-11-09)

- The phone Pinky draws around a mirror now turns with the screen: an iPad or iPhone in landscape, or an iPad in portrait, gets an outline of that shape on its page, in All phones and in recordings, with corners that stay the same turned.

## 2026.1109.8 (2026-11-09)

- Android devices without an H.265 encoder, such as some TVs, now show a picture over Wi-Fi instead of "No picture: … read codec: EOF". Thanks to NaNhkNaN.

## 2026.1109.7 (2026-11-09)

- Pinky now speaks Japanese: choose 日本語 in Settings › Advanced › Language, or let it follow a computer set to Japanese. Thanks to tarou-jp for the translation.
- On Linux, iPhones and iPads work over Wi-Fi: unplugged, Pinky moves them to Wi-Fi instead of showing them offline. Nothing to install beyond `usbmuxd`. Thanks to pseudoyu.

## 2026.1109.6 (2026-11-09)

- When you unplug an iPhone or iPad that doesn't come back over Wi-Fi, Pinky now says so within about 10 seconds on a Mac: Lost on Wi-Fi when your Mac's device service lost it, or Not seen on Wi-Fi, with what may be hiding it, such as another network, a guest network, or a VPN.

## 2026.1109.5 (2026-11-09)

- An iPhone's or iPad's Checkup now has a Wi-Fi item: whether its Wi-Fi connection is on and whether Pinky turned it on, why not if it isn't, and whether this computer sees the phone on the network now. Feedback diagnostics include it too.
- On iPhones with iOS 27, a blocked app's notification banner over another app is now hidden from agents too, and covered in their screenshots.
- Settings › Privacy now says that screen recordings show a blocked app's notifications and its card in recent apps as they were; recordings stay on your computer.

## 2026.1109.4 (2026-11-09)

- iPhones and iPads now work in landscape: taps, swipes and what Pinky reads of the screen land in the right place whichever way the device is turned, and the mirror shows the screen the right way up.
- On iPads whose screen is mounted sideways, such as the iPad Pro, taps no longer land in the wrong place, in portrait too.
- Through the WebDriverAgent helper, alerts that show in landscape can now be tapped where they appear, an iPad held upside down shows the right way up, and Back on an iPad goes back instead of opening dictation.
- Recordings of an iPhone or iPad play back the right way up, also when the device was turned while recording.

## 2026.1109.3 (2026-11-09)

- A blocked app's notifications are now hidden from agents in the notification shade, on the lock screen and in banners, and its card in recent apps too: the agent reads only that something is hidden there, and its screenshots, and those in Activity, cover it with a gray box.
- Notifications of an app you blocked by its name are now hidden as well, not only those of apps blocked by their package.
- Settings › Safety › Add app now offers the apps on your phones to pick from, and warns when the name you type matches none of them.
- More Chinese banks' credit card apps are blocked by default, such as 买单吧, 发现精彩, 动卡空间, 浦大喜奔 and 全民生活, and Shanghai Pudong Development Bank's app is now recognized by its package.
- A disk image you put in Pinky's folder yourself now gets the same offer to download the third-party mirror's image when Apple won't sign it.

## 2026.1109.2 (2026-11-09)

- When Apple won't sign the developer disk image of Xcode on your Mac, Add phone now also offers to download the image from the third-party mirror, and Pinky then uses that image instead of the refused one, also after a restart.
- When a phone drops for a moment and comes back, an agent's request now waits until Pinky has reconnected it, instead of failing as offline.
- On a slow computer, pairing over Wi-Fi no longer ends with "The phone didn't answer in time" when the phone paired but Pinky couldn't find it on the network; it asks for the address Wireless debugging shows, as on other computers.

## 2026.1109.1 (2026-11-09)

- On iOS 27.2, "Unlock from this computer" works again instead of failing with "unknown backlight state".
- On Linux, iPhones no longer get stuck in "Needs setup" because Pinky didn't trust Apple's signing server. A certificate problem with that server now shows the actual error instead of asking you to check the internet connection.
- On Linux with KDE Wallet, "Unlock from this computer" now asks for the wallet's password instead of failing with "Can't lock object".

## 2026.1108.25 (2026-11-08)

- One-time codes are now hidden from agents everywhere on the phone's screen, not only in notifications: a code in a text message, an email or an iPhone banner reaches the agent as ••••••, and screenshots agents get, and those in Activity, cover it with a gray box. Screen recordings, which stay on your computer, are not covered.
- Agents connected over HTTP keep the phone from one call to the next, also when their client never sends back the session id (such as Grok Bot) or opens a new connection for every call. No proxy in front of Pinky is needed any more.
- All phones now scrolls when you have more phones than fit in the window, and a last row that isn't full lines up under the phones above instead of sitting in the middle.
- With many phones, the sidebar's list of phones scrolls too, so Add phone, Activity, Settings and Stop all stay within reach.

## 2026.1108.24 (2026-11-08)

- When a phone isn't on your tailnet, Settings › Phones now says whether Tailscale isn't connected on it or is signed in to another account, with Set up Tailscale to open the steps.
- Pinky now says that Android runs one VPN at a time: another VPN or proxy app on the phone turns Tailscale off.
- Feedback diagnostics now include Tailscale's state and the Android devices on your tailnet, without their names.
- When pairing over Wi-Fi fails, Pinky now says why: the phone ended the pairing, nothing at that address was waiting to pair, the phone didn't answer, or the computer's adb is too old. It no longer says the pairing code didn't match when you scanned a QR code.
- A QR pairing that fails now offers Pair with a code instead next to Try again.
- adb's own words for a failed pairing now go to Pinky's log, so feedback diagnostics include them.

## 2026.1108.23 (2026-11-08)

- When Apple won't sign the developer disk image Pinky has for an iPhone (the images of Xcode 26 and earlier no longer work for iOS 27), Add phone and the phone's Checkup now say which image it was and what to do: install or update Xcode from the App Store and open it once. Pinky no longer retries the same image, and tries again as soon as a newer one is there.
- On iPhones, apps are now found by the name their home screen shows: a task for 微信 opens WeChat, not 微信读书. A name that is only part of other apps' names no longer opens one of them; agents are told which apps it may mean.
- iPhones connected through the WebDriverAgent helper: a locked phone no longer reads as an app with an empty screen, and lists such as WeChat's chats no longer repeat each row's texts or show rows hidden under the navigation bar.
- Agents that reach Pinky over HTTP from another computer can now hand it a file for an Android phone: `phone_files` takes an upload, a small file right in the call or a file of up to 200 MB through a one-time link, and pushing it to the phone still asks you first.

## 2026.1108.22 (2026-11-08)

- Settings › Safety now describes Hide codes from agents as it works: one-time codes in notifications are masked before an agent reads them. It never blurred the screen itself.

## 2026.1108.21 (2026-11-08)

- On an iPhone with iOS 27, agents now see the elements of web pages in Safari (links, buttons and fields) rather than only the text read from the picture, and a web page's password field counts as one, so agents can't type into it.
- On an iPhone with iOS 27, agents can open http and https links with `phone_launch`: they open in Safari, and Pinky judges them by Safari's rules too, so blocking Safari or leaving it out of an agent's scope stops its links. The Installs and links rule still asks.
- Deleting old activity now stops if Pinky cannot finish reading which screenshots belonged to those tasks, instead of deleting the tasks and leaving the screenshots behind.

## 2026.1108.20 (2026-11-08)

- Plugging in an iPhone or iPad now turns on its Wi-Fi connection, as Finder's "Show this iPhone when on Wi-Fi" does, so it keeps working over Wi-Fi when you unplug it; you no longer need Finder or Xcode for that.
- Over Wi-Fi, screenshots of an iPhone or iPad for approvals, history and agents now come from the live picture, in a fraction of a second instead of up to half a minute.
- On iPhones, Pinky no longer takes a picture of a blocked app you haven't allowed, not even in memory.
- On Linux, `pinkyctl daemon` no longer says it keeps the computer awake when the system refuses a moment later; it reports the refusal and tries again in 5 minutes.

## 2026.1108.19 (2026-11-08)

- Android phones now read text that apps draw without describing it, as in mini-programs and other pages inside super-apps, games and maps: Pinky reads it from the screen on macOS and Windows, and agents can read and tap it. `phone_observe` takes `ocr: true` to read all the text on the screen.
- A blocked app you allow for a task is read this way too: agents get its text, never a picture of it.
- Taps on Android where the screen says little, such as a "Pay" button that is a picture, are now checked against the text drawn there.
- Pinky now reads text from screen pictures on Windows too.

## 2026.1108.18 (2026-11-08)

- Pinky in Chinese now shows What's new and the update notes in Chinese.

## 2026.1108.17 (2026-11-08)

- Typing on an iPhone no longer adds a space where you didn't type one, as when text is added after a word.

## 2026.1108.16 (2026-11-08)

- `pinkyctl daemon` now keeps the computer awake while an agent task runs and while a phone is connected through Tailscale, as the app does, so it no longer needs `caffeinate`.

## 2026.1108.15 (2026-11-08)

- Watching a phone takes about a third of the CPU it did: the video's frames go to the GPU as the phone sends them, without converting each one first. Recordings play the same way.
- Screenshots saved from a phone's screen are at the phone's full resolution.
- Windows and Linux computers stay awake while an agent task runs, and while a phone is connected through Tailscale with Keep this computer awake for phones away on, as Macs already did.

## 2026.1108.14 (2026-11-08)

- iPhones and iPads now work over Wi-Fi too, on by default: one you use with Pinky keeps working when you unplug it, while it's on the same Wi-Fi as your computer, and a running task moves between the cable and Wi-Fi by itself. Turn it off in Settings › Phones.
- When Pinky finds an iPhone or iPad on Wi-Fi that it hasn't used, such as a family member's iPad paired with your computer, it shows it as Found on Wi-Fi and leaves it alone until you choose Use or Ignore.
- A locked iPhone or iPad on Wi-Fi shows as Locked on Wi-Fi, and Pinky connects once you unlock it; it leaves a sleeping phone alone, also one without a passcode, so its screen doesn't light up. An agent that needs it asks you to unlock it.
- Over Wi-Fi, Pinky reads the text on an iPhone's screen from the live picture, which is much quicker than a screenshot there (about 5 seconds).
- When a Mac's device service loses an iPhone on Wi-Fi, which it can do for up to an hour after the phone's connection broke once, Pinky notices, shows it as Lost on Wi-Fi and offers to restart the service: macOS asks for an administrator's password, and every iPhone and iPad disconnects for a second or two. To notice it, Pinky looks for your iPhones on the local network, so macOS may ask whether Pinky may do that.
- Add phone › iPhone says which steps need the cable: trusting the computer, and making the Developer Mode switch appear; the rest works over Wi-Fi.
- A phone Pinky is connecting to, or moving between the cable and Wi-Fi, shows as Connecting or Moving to Wi-Fi rather than offline.
- iPads are called iPads, with their model names.
- Use your Android phone when it's away from your computer's network, as when you're out and the computer is at home, through Tailscale: turn on Use away from home for the phone in Settings › Phones once while it's near the computer, and Pinky keeps reaching it wherever it is. Add phone › Android has the steps, with buttons to get or open Tailscale and to connect the phones on your tailnet.
- Pinky says what Use away from home opens before turning it on (the phone's adb listens on the network until it restarts) and turns it off with one click, or when you remove the phone.
- New in Settings › Phones: Keep this computer awake for phones away, on by default, so agents can still reach a phone connected through Tailscale.
- Through Tailscale, Pinky streams the phone's screen lighter (1 Mbit/s, 15 frames a second), since the phone's upload carries it.
- Android 10 phones, which have no Wireless debugging, can now leave the cable too: plug the phone in and choose Use Wi-Fi instead… in the ⋯ menu under its picture. Pinky reconnects it by itself when it comes back to the network, and after the phone restarts its page says to plug it in once and choose Use Wi-Fi again; Stop the Wi-Fi connection turns it off.

## 2026.1108.13 (2026-11-08)

- On Windows, a task's screen recording no longer takes in a moment of your own use of the phone from just before the task started or just after it ended.
- An iPhone below iOS 27 set to connect through CoreDevice now says in your language that it needs another connection, with a button to Settings › Phones.

## 2026.1108.12 (2026-11-08)

- An agent that connects to a phone waiting for a step (trusting the computer, allowing USB debugging, Developer Mode) now hears which step, instead of being told the phone is offline, and can ask you for it. Pinky's request card then says the agent goes on by itself once the phone is ready.
- An iPhone waiting for its passcode before the WebDriverAgent helper may control it now says so in your language.
- A phone that never connected fully and was unplugged leaves the phone list after two minutes, such as the extra entry Windows showed for a phone before its USB driver was installed.
- `pinkyctl devices` no longer prints an offline phone's reason twice, and lists phones that need setup as such.
- On phones that ask before installing apps over USB, the Update card for Pinky Companion now says why to update when it is signed with the old public key: anyone could make a fake update of it, and the new version is signed with Pinky's private key.

## 2026.1108.11 (2026-11-08)

- Pinky Companion's accessibility service is now on only while an agent works on the phone: it turns on as a task starts and off a few seconds after the task ends or you take over. Banking apps that refuse to run next to an accessibility service from outside an app store work again the rest of the time.
- Pinky Companion is now signed with Pinky's private key. Builds signed with the old key, which was public, are removed and installed again by themselves when the phone is free, and Pinky tells you; phones that ask before installing over USB wait for you to tap Update.
- The FAQ says which apps can notice Pinky Companion and what to do about it.

## 2026.1108.10 (2026-11-08)

- Pinky now knows when the WebDriverAgent helper on an iPhone stops working: with a free Apple ID its signature lasts 7 days. Two days before, the phone's page and its checkup remind you to install it again from Xcode, with how to do it.
- Once the helper's signature has run out, the iPhone shows as needing setup and says why, instead of failing to start the helper.
- An iPhone set to connect through the helper without it installed now says so in your language, with how to install it.
- Phones waiting for a step before Pinky can use them (allowing USB debugging, trusting the computer, Developer Mode, the developer disk image, a USB driver) now show as needing setup instead of offline, and their page says what to do in your language.
- A phone unplugged while it waited for a step now shows as offline instead of still asking for that step.

## 2026.1108.9 (2026-11-08)

- Pinky now mounts Apple's developer disk image on iPhones with iOS 27 by itself, in a second or two. This corrects 2026.1108.8: Xcode is no longer needed to mount it once.
- Fixed iPhone connections that could treat Pinky as an old host, which broke large transfers on that connection.
- The "You're in control" bar now sits above the phone in Pinky's window instead of over the top of its screen, so buttons there, such as an app's back button or menu, can be clicked from the computer.
- Clicking the phone in Pinky's window to take over now always finishes that tap on the phone (it could stay held down), and what you type right after reaches the phone.
- On a locked phone, the dimming and the "is locked" label step aside while the pointer is on the phone in Pinky's window, so you can unlock it there by hand.

## 2026.1108.8 (2026-11-08)

- A correction to the last release: on iOS 27, Pinky can't yet mount Apple's developer disk image by itself (it waits until it times out). Open Xcode once with the iPhone plugged in; the iPhone keeps the image when it restarts.
- Pinky now notices within a minute when an iPhone's developer disk image goes away while it stays plugged in, and checks again whenever an iPhone is plugged in or restarts, instead of still showing it as connected.
- Add phone › iPhone shows an iPhone that is already connected and ready, instead of looking for one.
- Pinky's log now records each step of preparing an iPhone: mounting the developer disk image (how, how long, and why it failed), the Developer Mode switch, the trust request and the image download.
- When something in Pinky gets in an agent's way, the agent now tells you afterwards and suggests reporting it with Help › Send Feedback…, leaving out what was on your screen. It never posts a report without your yes. Update in Settings › Agents gives your agents the new guide.
- On iPhones below iOS 27, Pinky now notices when the developer disk image is not mounted, instead of failing later when it starts the WebDriverAgent helper.

## 2026.1108.7 (2026-11-08)

- iPhones no longer need Xcode's Devices window: Pinky turns on what the iPhone needs for development by itself, and again after every restart. Add phone › iPhone walks you through the steps (trusting the computer, Developer Mode) and says what to do when one is stuck.
- Pinky uses Xcode's developer disk image when Xcode is installed. Without Xcode, Add phone can download the image, when you click Download, from github.com/doronz88/DeveloperDiskImage, a third-party copy of Apple's files, and checks every file against pinned checksums.
- Each iPhone's checkup now covers trusting the computer, Developer Mode and the developer disk image.
- On Linux, iPhones plugged in after Pinky started are found; before, Pinky had to be restarted. Add phone says when Windows lacks Apple's iPhone driver or Linux lacks usbmuxd.

## 2026.1108.6 (2026-11-08)

- You can now remove a phone from Pinky: Remove this phone… in the phone's ⋯ menu, on its page while it is offline, on its tile in All phones, by right-clicking it in the sidebar, or in Settings › Phones. Its saved PIN and settings go; its history stays unless you choose to delete it too.
- A phone you removed over Wi-Fi stays away until you pair it again or plug it in, even when adb reconnects it by itself. Pinky doesn't unpair the phone from your computer, so other apps that use it keep working.

## 2026.1108.5 (2026-11-08)

- Pinky now carries its own adb on macOS, Windows and Linux, so Android phones work without installing Android's platform tools. It still uses the adb you already run, such as Android Studio's or Homebrew's, also when Pinky is opened from the Finder.
- On Windows, Add phone shows Android phones that lack a USB driver and how to get one; on Linux, it shows the steps that let Pinky reach the phone over USB.
- Add phone › Android walks you through connecting your phone step by step for its brand, such as Xiaomi, OPPO, vivo, HONOR, HUAWEI, Samsung and Pixel, including each brand's extra switches.
- Each phone has a checkup that lists what works and how to fix what doesn't, such as Xiaomi's USB debugging (Security settings) or a phone that needs a restart. When adb itself is the problem, Pinky says so first.
- Pinky finds the agents on your computer, such as Claude Code, Claude Desktop, Codex and Cursor, and connects them with one click, the Pinky skill included. Until one is connected, Pinky offers to do it.
- Each release includes Pinky's MCP bundle for Claude Desktop, which adds Pinky with a double-click.

## 2026.1108.4 (2026-11-08)

- You can now choose how Pinky connects to each iPhone in Settings › Phones: Automatic, CoreDevice (nothing to install, iOS 27 and later) or the WebDriverAgent helper. Through the helper the iPhone shares no screen, so banking apps that refuse screen sharing (DBS digibank and China Merchants Bank in testing) keep working while Pinky is connected.
- Unlocking an iPhone from the computer also works through the WebDriverAgent helper.
- When an iPhone asks for its passcode before the WebDriverAgent helper can start, Pinky says so in Settings and connects once you enter it, instead of starting the helper over and over.

## 2026.1108.3 (2026-11-08)

- On iPhone, alerts and notification banners now reach the agent, and Pinky's code masking and hiding of blocked apps' notifications apply to them.
- On iPhone, Pinky stops the live picture after 30 seconds when nobody is watching, so banking apps that refuse to run while the screen is shared, and the camera and microphone, work for you again.
- On iPhone, the app in front is always named by its bundle id, also right after a session starts and while Face ID is on screen, so blocked apps stay blocked.
- On iPhone, elements keep their numbers between reads, picker wheels read as their values, and rows hidden behind the tab bar are left out.
- On iPhone, the agent is told when part of the screen, such as some apps' passcode keys, is hidden from screenshots and the mirror.
- Unlocking an iPhone from the computer swipes again when notifications cover the lock screen.

## 2026.1108.2 (2026-11-08)

- By default, Pinky now asks only before payments (Settings › Safety › When should agents ask you?). Installs that never changed this setting move to it too; if you changed any safety setting, your choice stays.
- Buttons that move money, such as Sell, Swap, Trade, Repay, Borrow, Stake, Withdraw, Transfer, 买入, 卖出 and 还款, now count as payments and ask, with or without an amount on the screen.
- "Bin" and "Remove" buttons now count as deletions, as "Delete" and "Trash" do.
- More banking, payment and crypto apps are blocked by default, among them DBS, OCBC, UOB, Trust Bank, OKX, Coinbase, Crypto.com and ether.fi; Singpass is blocked with the password managers.
- An app blocked as part of a family of apps, such as all of one bank's apps, can now be unblocked on its own in Settings › Safety.
- Edits to the rules files take effect while Pinky runs, and Settings › Safety shows them.
- Opening a blocked banking or wallet app now asks you instead of failing. If you allow it, the agent may open and read it for the rest of that task; what your rules ask about there still asks, and Pinky keeps no screenshots or recordings of it.
- An agent can start a task read-only, and a new agent that asks for read-only gets that choice first. The scope menu says plainly what each choice allows.

## 2026.1108.1 (2026-11-08)

- Approvals now show the whole phone screen with what the agent wants to tap marked, next to a close-up you can read, so you can tell the app, the page and the button at a glance. Swipes show as an arrow.
- Click an approval's screenshot to see it larger.
- Approval cards in the main window show the screenshot when that phone isn't the one on screen.
- When an approval has no screenshot, it says why, such as the phone being locked or the app being blocked.
- Taps on buttons whose words only show in the picture, such as in web pages inside apps, are now checked against your rules too.
- Waiting for text matches whole words, so waiting for "Stop" no longer stops at "Stopwatch".
- After Home or opening an app, the agent's next step waits until the app in front has changed.
- Unlocking a phone from the computer no longer ends a handoff that asked you for more than the unlock.
- A tap on a dimmed backdrop, such as the area around a menu, lands where nothing lies on it instead of on what is underneath.
- Agents keep their Pinky connection when Pinky quits and pick up again when it reopens.
- The Windows and Linux packages now carry FFmpeg, so the live mirror works there without installing it.
- The command that Homebrew and the Linux packages install is now `pinkyctl`, because `pinky` is also a coreutils command: it took Pinky's place on many Macs and kept the `.deb` from installing on Debian and Ubuntu. Agents set up with `pinky mcp` need `pinkyctl mcp`, or the command in Settings › Agents.
- Settings › Agents shows ready-to-copy commands that add Pinky to Claude Code and other agents.

## 2026.1107.9 (2026-11-07)

- Unlocking an iPhone from the computer works again after the phone locked during a long session.
- Typing Chinese or other non-English text on an iPhone no longer fails the first time after a quiet spell.

## 2026.1107.8 (2026-11-07)

- On iPhones below iOS 27 without the WebDriverAgent helper, Pinky now says up front that taps, swipes and typing need the helper; it can still show and read the screen and open apps.

## 2026.1107.7 (2026-11-07)

- Pairing a phone over Wi-Fi no longer hangs on "Pairing…" when Android's adb tools have stopped seeing the network, as they do after the computer's network changes (for example when an iPhone is plugged in): Pinky now finds the phone itself.
- Phones paired over Wi-Fi come back by themselves when they return to the network, even when adb no longer sees it.
- When pairing over Wi-Fi takes long, Add phone says what is likely wrong, such as the phone not showing up on the network or, on a Mac, Pinky not allowed to use the local network, and offers to pair with a code instead.
- Pairing with a code now also works on networks where Pinky can't find the phone: it asks for the address Wireless debugging shows and connects there.
- On a Mac, Pinky asks for Local Network access to find Android phones over Wi-Fi, and says why.

## 2026.1107.6 (2026-11-07)

- The mirror no longer stays on "Connecting…" when you come back to a phone after a while.
- Unblocking an app in Settings › Safety now works for apps that are blocked by their id, such as Wise.
- An agent's tap on an element keeps working when the iPhone reports that element under another parent or role.
- Waiting for text now also finds text that only shows in the picture, such as in web pages inside apps.
- When a task lists app names that match no installed app, the agent is told which ones.

## 2026.1107.5 (2026-11-07)

- On Windows, Pinky now installs updates by itself, as on macOS: when you're not using it, or when you click Update now, it runs the new installer quietly, starts the new version and shows what's new, and goes back to the version you had if the new one doesn't start. Agents connected through `pinky mcp` stay connected.
- On macOS, Pinky installs an update only after checking that Apple notarized it.
- When an update fails, Settings › Advanced says why, for example that the new version didn't start.
- Homebrew no longer prints a deprecation warning about Pinky's cask with every `brew` command.
- When an agent asks you a question, the answer no longer offers "Allow for this task", which granted nothing there.
- Activity marks the steps that ran without asking because of an Always allow.

## 2026.1107.4 (2026-11-07)

- iPhones on iOS 17.4 to 26 can now be connected. With the WebDriverAgent helper, installed once from Xcode, agents can do everything they do on iOS 27; this is tried on the iOS 18 and 26 Simulators, not yet on such a phone.
- Without the helper, Pinky shows and reads an older iPhone's screen and opens apps; it cannot swipe or type there.
- Pinky says why it cannot drive an iPhone on iOS 17.3 or earlier.

## 2026.1107.3 (2026-11-07)

- Turning Record the screen, Keep activity, Read-only mode or usage statistics on and off quickly no longer leaves Pinky with an earlier choice than the switch shows.

## 2026.1107.2 (2026-11-07)

- "Always allow" on an approval now lasts: in that app, Pinky stops asking about steps like that one, in later tasks and after a restart. Settings › Safety › Always allowed lists them, and any of them can be made to ask again. It never lifts a block, read-only mode or what an agent may do, and "Ask before every action" still asks about everything.
- "Always allow" is offered only where Pinky can keep it: not for an agent's own questions, copying files, or opening apps and links.
- Settings › Safety has a rules editor: add, change, reorder and remove your own rules (apps, categories, actions, button text; Ask, Block or Allow), with Pinky's built-in rules shown alongside. Removing or loosening a rule that blocks asks you first.
- Buttons that show only an icon, such as a paper plane that sends or a trash can that deletes, now ask before they commit, like buttons with words.

## 2026.1107.1 (2026-11-07)

- The first release of Pinky: AI agents such as Claude Code, Codex and Cursor operate your iPhone or Android phone over MCP while you watch on your computer, approve risky steps, take over and look back at what happened.
- Android phones over USB or Wi-Fi. The companion app, which Pinky offers to install, reads the screen in about 70 ms and types any language; on Android 13 and later agents work on a background display while the phone stays locked.
- iPhones on iOS 27 or later over USB, with nothing to install on the phone.
- Unlock a locked phone from the computer with Touch ID (Windows Hello on Windows); agents never see the PIN.
- History of every task, with recordings and export.
- Pinky for macOS 12 or later (Apple silicon and Intel), Windows and Linux; on macOS, `brew install --cask diygod/tap/pinky` also installs the `pinky` command.
- Pinky keeps itself up to date: it tells you when a new version is out and what changed, installs it by itself when you're not using it (Settings › Advanced › Update automatically), and shows what's new afterwards.
- Pinky Companion on your Android phones is updated by itself while the phone is free, and its service stays on.
- Pinky shares anonymous usage statistics and crash reports to help find what to fix: counts such as tasks run and tools used, never your screens, what you type, app names, phone names or PINs. Turn it off in Settings › Privacy or with `DO_NOT_TRACK=1`; Settings › Privacy › What's shared shows exactly what goes out.
- Help › Send Feedback… opens an issue on GitHub with your message (and diagnostics, if you choose to attach them) filled in; you review it there and submit it yourself.
- Pinky speaks Simplified Chinese. It follows your Mac's language, and Settings › Advanced › Language switches between System, English and Simplified Chinese right away, without a restart.
- In Chinese, agents' steps, approval requests, phone status lines and update messages read in Chinese too. What agents and apps write stays as written.
