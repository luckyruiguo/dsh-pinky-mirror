# Bundled scrcpy server

`scrcpy-server-v5.0.1` is the unmodified server of [scrcpy](https://github.com/Genymobile/scrcpy) 5.0.1, copyright Genymobile and Romain Vimont, licensed under the Apache License 2.0 (see [LICENSE](LICENSE)). Pinky pushes it to the phone over adb and speaks its protocol directly; no scrcpy client code is used.

| | |
| --- | --- |
| Version | 5.0.1 (git `dc8a55a24c27ae78ae202b5a3288da681d0f141a`) |
| Source | https://github.com/Genymobile/scrcpy/releases/download/v5.0.1/scrcpy-server-v5.0.1 |
| SHA-256 | `764eb6f79811d5211fe9df341120882ba9994c7a61b897d7bf3fb662e53bc536` |

The server refuses a client of another version, so `scrcpy.Version` in `../scrcpy.go` must match the file name. To upgrade:

1. Download the new `scrcpy-server-vX.Y.Z` from the release page and check its SHA-256 against the release notes.
2. Replace the file here, update the table above, `Version` and the `//go:embed` line.
3. Read the server's `Options.java`, `Streamer.java`, `ControlMessageReader.java` and `DeviceMessageWriter.java` for protocol changes, then run the unit tests and the `device` tests on a phone.
