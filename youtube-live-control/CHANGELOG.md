# Changelog

## 1.0.0

Initial release.

- One **YouTube Live** MQTT device: a **Broadcast** selector (upcoming broadcasts plus *New stream…*) with a detail panel — **Title**, **Scheduled start**, **Thumbnail**, **Stream health** / **Broadcast status** / **Viewers** sensors and **Save** / **Create** / **Go Live** / **End Stream** buttons — instead of one entity per broadcast.
- OAuth 2.0 with the `youtube.force-ssl` scope against your own Google OAuth client; one-time consent through the ingress web UI, refresh token persisted in `/data/token.json`.
- Every write is verified first and read back before Home Assistant is updated; nothing is published optimistically. State is retained, commands are not, and retained command replays are dropped.
- **Go Live** is only available while the encoder's stream is `active`; **End Stream** only while live and after the stream has stopped. While YouTube's `streamStatus` lags a stopped encoder, the status sensor shows `live (waiting for stream to stop)`.
- Broadcasts are created and saved with `enableAutoStart` and `enableAutoStop` explicitly false — transitions happen only through the buttons.
- Quota-aware, presence-driven polling in three tiers, all only while a broadcast is selected: idle baseline (10 min), live cadence (60 s) and a fast window (3 s for 5 minutes, ~40 units/min). The fast window is a **Fast refresh** switch with a minutes-remaining sensor: auto-armed by any panel interaction — including refused button presses — re-armed on each interaction, and switched off by the add-on when the timer expires.
- Runtime behaviour is not hard-coded: poll cadences and the fast window are configuration number entities, applied immediately and persisted in `/data/settings.json`. Add-on options carry only restart-scoped infrastructure.
