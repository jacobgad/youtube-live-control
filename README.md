# YouTube Live Control

Home Assistant add-on that exposes one **YouTube live-broadcast control panel** as native MQTT entities. A volunteer on an iPad picks or creates a scheduled broadcast, edits the title, goes live once OBS is streaming, and ends it after the service. Dashboards, automations and access control are Home Assistant's job; the add-on is only the YouTube driver.

User documentation: [`youtube-live-control/DOCS.md`](youtube-live-control/DOCS.md).

## How it works

```text
Home Assistant ──MQTT──▶ Mosquitto ◀──MQTT── YouTube Live Control ──HTTPS──▶ YouTube Data API v3
      │                                        │
      └─ ingress web UI ───────────────────────┼─ one-time OAuth consent (youtube.force-ssl)
                                               ├─ refresh token in /data/token.json
                                               ├─ tiered polling: idle 10 min · live 60 s · fast 3 s
                                               └─ commands: verify → write → read back
```

- **One selector, one detail panel.** A **Broadcast** select (upcoming + *New stream…*) drives shared Title / Scheduled start / Thumbnail fields, Stream health / Broadcast status / Viewers sensors, and Save / Create / Go Live / End Stream buttons. Changing the selection republishes every detail entity so the panel pre-fills. There is never one entity per broadcast.
- **Writes are verified.** Before any command the broadcast and its bound stream are re-read from the API; after the write, the result is read back before Home Assistant is updated. Nothing is published optimistically.
- **Transitions are gated on reality.** Go Live requires the encoder's `streamStatus` to be `active`; End Stream requires being live with the stream no longer active. `enableAutoStart`/`enableAutoStop` are always written as `false`, so only the buttons ever transition a broadcast. While `streamStatus` lags a stopped encoder, the status sensor shows `live (waiting for stream to stop)`.
- **Nothing transitions on its own.** State is retained, commands are not, retained replays are dropped: restarts of the add-on, the broker or Home Assistant republish state but never start or stop a broadcast.
- **Quota-aware, presence-driven polling.** Three tiers, all only while a broadcast is selected: a 10-minute idle baseline, a 60-second cadence while live, and a 3-second fast window (~40 units/min, capped at 5 minutes) armed by any panel interaction — including refused button presses — or by the **Fast refresh** switch, which the add-on itself turns off on expiry. A countdown sensor shows minutes remaining.
- **Nothing user-facing is hard-coded.** Poll cadences and the fast window are configuration number entities, applied at runtime and persisted in `/data/settings.json`; the add-on options carry only restart-scoped infrastructure (OAuth client, URLs, directories).

Single static Go binary on plain Alpine. The only web surface is the ingress OAuth console plus the `:8098` redirect endpoint Google needs.

## MQTT contract

Prefix `ylc/`. State is retained; commands (`…/set`, `…/press`) are not, and retained messages are never acted on.

| Topic | Purpose |
| --- | --- |
| `ylc/controller/availability` | controller online/offline; also the Last Will |
| `ylc/auth/state` | `authorized` / `unauthorized`; command entities list it as an availability |
| `ylc/broadcast/{state,set}` | Broadcast select (labels; *New stream…* sentinel) |
| `ylc/title/{state,set}` | Title text |
| `ylc/scheduled_start/{state,set}` | Scheduled start text, `YYYY-MM-DD HH:MM` local |
| `ylc/thumbnail/{state,set}` | Thumbnail select (*Keep current* + files) |
| `ylc/fast_mode/{state,set}` | Fast refresh switch (`ON`/`OFF`; add-on publishes `OFF` on expiry) |
| `ylc/fast_mode_remaining/state` | minutes left in the fast window |
| `ylc/{stream_health,broadcast_status,viewers}/state` | sensors |
| `ylc/{save,create,go_live,end_stream}/press` | buttons |
| `ylc/{save,create,go_live,end_stream}/availability` | per-button gates |
| `ylc/<setting>/{state,set}` | settings numbers: `list_poll_minutes`, `fast_poll_seconds`, `fast_mode_minutes`, `live_poll_seconds`, `idle_poll_minutes` |

Home Assistant device identifier `ylc:controller`; entity unique IDs `youtube_live_control_<object>`; discovery configs under `homeassistant/<component>/youtube_live_control/<object>/config` (republished when select options change).

## YouTube API usage

| Call | When | Units |
| --- | --- | --- |
| `liveBroadcasts.list` (upcoming, active) | list poll | 1 + 1 |
| `liveBroadcasts.list` (id) + `liveStreams.list` | status poll (tiered), and as the verify/read-back around every command | 1 + 1 |
| `videos.list` (concurrentViewers) | status poll while live | 1 |
| `liveBroadcasts.insert` / `update` / `bind` / `transition`, `thumbnails.set` | buttons | 50 each |

Broadcasts created by the add-on disable the monitor stream (`ready → live` in one transition) and are bound to the channel's reusable stream key so OBS's fixed key attaches. Studio-created broadcasts with a monitor stream are taken through `testing` automatically.

## Development

Requires Go ≥ 1.27, [golangci-lint](https://golangci-lint.run) v2, Docker for images.

```bash
cd youtube-live-control
go test -race ./...
golangci-lint run
docker build --build-arg BUILD_VERSION=dev .
```

Run outside the Supervisor by setting `MQTT_HOST` (plus `MQTT_PORT`/`MQTT_USERNAME`/`MQTT_PASSWORD`/`MQTT_SSL`), `YLC_OPTIONS_PATH` to a local options JSON, `YLC_TOKEN_PATH` for the token file and `YLC_SETTINGS_PATH` for the runtime settings file.
