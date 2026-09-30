# YouTube Live Control

Home Assistant add-on that runs a channel's scheduled YouTube live streams. Producers schedule from presets on the dashboard; volunteers go live and end from the same dashboard; the web UI holds presets, thumbnails and one-off edits.

User documentation: [`youtube-live-control/DOCS.md`](youtube-live-control/DOCS.md).

## Design

```text
Home Assistant ──MQTT──▶ Mosquitto ◀──MQTT── youtube-live-control ──HTTPS──▶ YouTube Data API v3
      │                                        │
      └─ ingress web UI ───────────────────────┼─ OAuth consent · presets · images · broadcast editor
                                               ├─ SQLite (/data/ylc.sqlite) + /data/images
                                               ├─ polling: idle · live · fast (presence-driven)
                                               └─ commands: verify → write → read back
```

- **Two MQTT devices.** *YouTube Live Scheduling* creates broadcasts from presets (Preset, Date, Time, Schedule). *YouTube Live* is the selected broadcast: Broadcast select, Title and Privacy applied immediately, a Stage enum sensor, timestamp, image and binary sensors, Go Live and End Stream. Every entity is a core Home Assistant platform.
- **Nothing is optimistic.** Commands re-read the broadcast and stream, write, then read back before publishing. State topics are retained, command topics are not, replays are dropped. `enableAutoStart`/`enableAutoStop` are always false; Go Live needs the stream `active`, End Stream needs it stopped.
- **Nothing selects itself.** The Broadcast select is sorted live-first then soonest; only a person changes it.
- **Polling follows people.** Idle, live and fast tiers; any panel interaction arms the fast window, which expires on its own.
- **Storage is one database and one directory.** Presets, image records, settings and the refresh token in SQLite with a versioned schema; thumbnails as files.

## MQTT contract

Prefix `ylc/`. State retained, commands not.

| Topic | Purpose |
| --- | --- |
| `controller/availability` | online/offline; also the Last Will |
| `auth/state`, `channel/state` | `authorized`/`unauthorized`; connected channel |
| `broadcast/{state,set,attributes}` | Broadcast select; attributes `id`, `scheduled_start`, `privacy`, `lifecycle`, `thumbnail_url`, `watch_url` |
| `title/{state,set}`, `privacy/{state,set}` | written to YouTube on change |
| `stage/state` | `no_broadcast` `no_stream_key` `waiting_for_encoder` `ready_to_go_live` `starting` `live` `stream_stopping` `ready_to_end` `ending` `ended` |
| `scheduled_start/state` | RFC 3339, or `None` |
| `thumbnail/{url,availability}` | image entity |
| `live/state`, `encoder/state` | `ON`/`OFF` |
| `{go_live,end_stream}/{press,availability}` | buttons and their gates |
| `fast_mode/{state,set}`, `fast_mode_remaining/state` | fast-refresh window |
| `{stream_health,broadcast_status}/state` | diagnostics |
| `preset/{state,set}`, `date/{state,set}`, `time/{state,set}` | scheduling inputs (`date`/`time` platforms, HA ≥ 2026.5) |
| `schedule/{press,availability}` | Schedule button and gate |

Devices `ylc:controller` (entities `youtube_live_control_*`) and `ylc:scheduling` (entities `youtube_live_scheduling_*`, `via_device` the former). Discovery under `homeassistant/<component>/<node>/<object>/config`.

## YouTube API calls

| Call | When | Units |
| --- | --- | --- |
| `liveBroadcasts.list` upcoming + active | list poll | 2 |
| `liveBroadcasts.list` by id, `liveStreams.list` by id | status poll; before and after every command | 1 + 1 |
| `liveStreams.list` mine, `channels.list` mine | web UI forms, connection | 1 |
| `liveBroadcasts.insert` / `update` / `delete` / `bind` / `transition`, `thumbnails.set` | Schedule, edits, buttons | 50 |

## Development

Go ≥ 1.27, golangci-lint v2, Docker for images. CI runs the add-on linter, lint and race tests, and builds both architectures.

```bash
cd youtube-live-control
go test -race ./...
golangci-lint run
```

Outside the Supervisor set `MQTT_HOST` (and `MQTT_PORT`, `MQTT_USERNAME`, `MQTT_PASSWORD`, `MQTT_SSL`), `YLC_OPTIONS_PATH`, `YLC_DATABASE_PATH` and `YLC_IMAGES_DIR`.
