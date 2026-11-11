# Pinky input test

A small Android app (`app.pinky.inputtest`) with text fields that take input in different ways, for checking how Pinky types into apps that do not behave like an ordinary `EditText`. It holds no data and asks for no permissions. It was written for GitHub issue #31, where WeChat's chat input on a background display took no text.

| Field (resource id) | What it is | How text reaches it |
| --- | --- | --- |
| `plain` | An ordinary `EditText` | `ACTION_SET_TEXT`, keys, paste |
| `no_set_text` | An `EditText` that refuses `ACTION_SET_TEXT`, as some apps' own subclasses do | Its input connection, keys, paste |
| `no_commit` | Refuses `ACTION_SET_TEXT` and drops what its input connection commits | Only a paste |
| `chat` | A `View` with its own input connection, built like a messaging app's chat input; refuses `ACTION_SET_TEXT` and `ACTION_PASTE`, reports its hint ("Message") as its text while empty, and sends the text on the keyboard's action (or Enter) | Its input connection, keys |
| `short_field` | An `EditText` of at most 4 characters | Like `plain`, cut to 4 characters |
| `dead` | A chat input that takes no text at all | Nothing |

The line at the bottom (`echo`) shows what each field holds and what `chat` last sent, so a test reads the result from the screen even where a field does not report its text.

Build it with the companion's Gradle wrapper (it needs `ANDROID_HOME`):

```sh
android/companion/gradlew -p android/inputtest assembleDebug
```

The APK is `build/outputs/apk/debug/pinky-inputtest-debug.apk`. `TestEmulatorBackgroundTypeFallback` (`internal/driver/android/typing_device_test.go`) installs it on an emulator and types into every field on a background display; its comment has the commands. Use it on emulators, or on a real phone only with its owner's yes.
