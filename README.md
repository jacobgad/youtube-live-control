# YouTube Live Control

Home Assistant add-on for running a channel's scheduled YouTube live streams. Producers **schedule from presets** on the dashboard (preset · date · time · Schedule); volunteers **operate** from the dashboard (Broadcast · Title · Privacy · **Stage** · Go Live / End Stream). Presets, descriptions, thumbnails and off-pattern dates are set up in the add-on's web UI. Dashboards, automations and access control are Home Assistant's job; the add-on is only the YouTube driver.

User documentation: [`youtube-live-control/DOCS.md`](youtube-live-control/DOCS.md).

## How it works

```text
Home Assistant ──MQTT──▶ Mosquitto ◀──MQTT── YouTube Live Control ──HTTPS──▶ YouTube Data API v3
      │                                        │
      └─ ingress web UI ───────────────────────┼─ OAuth consent · presets · schedule/edit broadcasts
                                               ├─ SQLite (/data/ylc.sqlite) + /data/images
                                               ├─ tiered polling: idle 10 min · live 60 s · fast 3 s
                                               └─ commands: verify → write → read back
```

- **Two devices, platform-native entities.** *YouTube Live Scheduling* (Preset / Date / Time / Schedule) creates from presets; *YouTube Live* is the selected broadcast: Broadcast select (sorted live-first, soonest-first; nothing selects automatically), Title and Privacy applied immediately, a **Stage** enum sensor, a timestamp **Scheduled start**, **Live** / **Encoder connected** binary sensors, Go Live / End Stream. A core image entity carries the thumbnail; the watch URL rides along as an attribute. Everything renders with core Lovelace cards; there is never one entity per broadcast.
- **Writes are verified.** Before any command the broadcast and its bound stream are re-read from the API; after the write, the result is read back before Home Assistant is updated. Nothing is published optimistically.
- **Transitions are gated on reality.** Go Live requires the encoder's `streamStatus` to be `active`; End Stream requires being live with the stream no longer active. `enableAutoStart`/`enableAutoStop` are always written as `false`, so only the buttons ever transition a broadcast. While `streamStatus` lags a stopped encoder, Stage shows `stream_stopping`.
- **Nothing transitions on its own.** State is retained, commands are not, retained replays are dropped: restarts of the add-on, the broker or Home Assistant republish state but never start or stop a broadcast.
- **Quota-aware, presence-driven polling.** Three tiers, all only while a broadcast is selected: a 10-minute idle baseline, a 60-second cadence while live, and a 3-second fast window (~40 units/min, capped at 5 minutes) armed by any panel interaction — including refused button presses — or by the **Fast refresh** switch, which the add-on itself turns off on expiry. A countdown sensor shows minutes remaining.
- **Configuration and control are separate.** Poll cadences and the fast window are add-on options; the MQTT device carries only what operates YouTube.

Everything the add-on remembers — presets, settings, the refresh token — lives in one SQLite file (`/data/ylc.sqlite`, pure-Go driver, schema versioned via `user_version`) with thumbnails in `/data/images/`: two paths to back up, one schema to migrate. Single static Go binary on plain Alpine. The only web surface is the ingress OAuth console plus a `:8098` redirect endpoint. Google's OAuth policy only allows plain-`http` redirects to localhost, so by default consent uses a **Desktop app** client with a `http://localhost:8098` redirect and the volunteer pastes the resulting URL back into the console; an https `external_url` in front of `:8098` makes the redirect complete on its own.

## MQTT contract

Prefix `ylc/`. State is retained; commands (`…/set`, `…/press`) are not, and retained messages are never acted on.

| Topic | Purpose |
| --- | --- |
| `ylc/controller/availability` | controller online/offline; also the Last Will |
| `ylc/auth/state` | `authorized` / `unauthorized`; command entities list it as an availability |
| `ylc/channel/state` | title of the connected YouTube channel |
| `ylc/broadcast/{state,set}` | Broadcast select (labels); `ylc/broadcast/attributes` carries id, scheduled_start, privacy, lifecycle, thumbnail_url, watch_url |
| `ylc/thumbnail/{url,availability}` | image entity source URL and availability |
| `ylc/title/{state,set}` | Title text, written to YouTube on Enter |
| `ylc/privacy/{state,set}` | Privacy select (`public` / `unlisted` / `private`), written immediately |
| `ylc/stage/state` | Stage enum: `no_broadcast` `no_stream_key` `waiting_for_encoder` `ready_to_go_live` `starting` `live` `stream_stopping` `ready_to_end` `ending` `ended` |
| `ylc/scheduled_start/state` | RFC 3339 timestamp |
| `ylc/{live,encoder}/state` | binary sensors (`ON`/`OFF`) |
| `ylc/fast_mode/{state,set}` | Fast refresh switch (`ON`/`OFF`; add-on publishes `OFF` on expiry) |
| `ylc/fast_mode_remaining/state` | minutes left in the fast window |
| `ylc/{stream_health,broadcast_status}/state` | diagnostic sensors |
| `ylc/{go_live,end_stream}/press` | buttons |
| `ylc/{go_live,end_stream}/availability` | per-button gates |
| `ylc/preset/{state,set}` | Preset select |
| `ylc/date/{state,set}`, `ylc/time/{state,set}` | `date` / `time` entities, ISO values (`None` while unset) |
| `ylc/schedule/press`, `ylc/schedule/availability` | Schedule button and gate |

Device identifiers `ylc:controller` (entities `youtube_live_control_<object>`) and `ylc:scheduling` (entities `youtube_live_scheduling_<object>`, `via_device` the former); discovery configs under `homeassistant/<component>/<node>/<object>/config`, republished when select options change.

## YouTube API usage

| Call | When | Units |
| --- | --- | --- |
| `liveBroadcasts.list` (upcoming, active) | list poll | 1 + 1 |
| `liveBroadcasts.list` (id) + `liveStreams.list` | status poll (tiered), and as the verify/read-back around every command | 1 + 1 |
| `liveStreams.list` (mine), `channels.list` (mine) | web UI forms, connection | 1 |
| `liveBroadcasts.insert` / `update` / `bind` / `transition`, `thumbnails.set` | Schedule, web UI actions, Title/Privacy edits, buttons | 50 each |

Broadcasts created by the add-on disable the monitor stream (`ready → live` in one transition) and are bound to the channel's reusable stream key so OBS's fixed key attaches. Studio-created broadcasts with a monitor stream are taken through `testing` automatically.

## Development

Requires Go ≥ 1.27, [golangci-lint](https://golangci-lint.run) v2, Docker for images. CI runs the same gate on every push, validates the add-on config and builds the image for both supported architectures.

```bash
cd youtube-live-control
go test -race ./...
golangci-lint run
docker build --build-arg BUILD_VERSION=dev .
```

Run outside the Supervisor by setting `MQTT_HOST` (plus `MQTT_PORT`/`MQTT_USERNAME`/`MQTT_PASSWORD`/`MQTT_SSL`), `YLC_OPTIONS_PATH` to a local options JSON, `YLC_DATABASE_PATH` and `YLC_IMAGES_DIR` for local storage.
