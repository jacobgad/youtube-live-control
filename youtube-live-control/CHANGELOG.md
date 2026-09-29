# Changelog

## 1.0.1

- **Save no longer resets other broadcast settings.** The update echoed only the fields the add-on knows about, and YouTube overwrites every mutable property in a part it receives — so DVR, latency, embed and caption settings on a Studio-created broadcast were reset on every Save. The broadcast is now written back exactly as fetched with only the title, scheduled start, privacy and the (always-false) auto start/stop flags changed.
- `live (waiting for stream to stop)` now shows whenever the broadcast is live and YouTube still reports the stream `active` — exactly the condition that keeps **End Stream** unavailable — rather than only when the health verdict had already dropped to `noData`.
- Google client secret and OAuth tokens can no longer appear in formatted log output.
- Title length is counted in characters, not bytes, matching Home Assistant's 100-character limit for non-Latin titles; surrounding whitespace is trimmed.
- Token and settings files are written atomically; a failed publish is retried on the next update rather than deduped away; shutdown no longer waits behind an in-flight publish sweep; the OAuth code exchange completes even if the browser tab is closed mid-way; a re-consent that lands during a failed token refresh is kept.
- CI on GitHub Actions: add-on config validation, lint and race-test gate, and image builds for both architectures on every push.

## 1.0.0

Initial release.

- One **YouTube Live** MQTT device: a **Broadcast** selector (upcoming broadcasts plus *New stream…*) with a detail panel — **Title**, **Scheduled start**, **Thumbnail**, **Stream health** / **Broadcast status** / **Viewers** sensors and **Save** / **Create** / **Go Live** / **End Stream** buttons — instead of one entity per broadcast.
- OAuth 2.0 with the `youtube.force-ssl` scope against your own Google OAuth client; one-time consent through the ingress web UI, refresh token persisted in `/data/token.json`.
- Every write is verified first and read back before Home Assistant is updated; nothing is published optimistically. State is retained, commands are not, and retained command replays are dropped.
- **Go Live** is only available while the encoder's stream is `active`; **End Stream** only while live and after the stream has stopped. While YouTube's `streamStatus` lags a stopped encoder, the status sensor shows `live (waiting for stream to stop)`.
- Broadcasts are created and saved with `enableAutoStart` and `enableAutoStop` explicitly false — transitions happen only through the buttons.
- Quota-aware, presence-driven polling in three tiers, all only while a broadcast is selected: idle baseline (10 min), live cadence (60 s) and a fast window (3 s for 5 minutes, ~40 units/min). The fast window is a **Fast refresh** switch with a minutes-remaining sensor: auto-armed by any panel interaction — including refused button presses — re-armed on each interaction, and switched off by the add-on when the timer expires.
- Runtime behaviour is not hard-coded: poll cadences and the fast window are configuration number entities, applied immediately and persisted in `/data/settings.json`. Add-on options carry only restart-scoped infrastructure.
