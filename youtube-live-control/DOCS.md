# YouTube Live Control

Runs a channel's scheduled YouTube live streams from Home Assistant.

- **Schedule** from the dashboard: pick a **preset**, confirm date and time in the native pickers, press **Schedule**. Presets (title pattern, description, privacy, stream key, thumbnail, usual day and time) are set up once in the add-on's web UI, which is also where descriptions, thumbnails and off-pattern dates are edited.
- **Operate** on the day: a volunteer picks the stream (it's the first option), fixes the title if needed, presses **Go Live** once OBS is streaming and **End Stream** after the service. One **Stage** sensor says what to do next.

## Installation

1. **Settings → Add-ons → Add-on Store → ⋮ → Repositories**, add this repository's URL.
2. Install **YouTube Live Control**.
3. Create a Google OAuth client (below), enter it on the **Configuration** tab, **Start** the add-on.
4. Open the add-on's web UI — the **Open Web UI** button on the add-on's Info tab, or **YouTube Live** in the sidebar once *Show in sidebar* is on — and connect the channel's Google account once.
5. Create a preset in the web UI, then schedule from the dashboard. Entities appear under **Settings → Devices & services → MQTT** as two devices: **YouTube Live** and **YouTube Live Scheduling**.

Requires Home Assistant **2026.5 or newer** (for the MQTT date and time entities), the **Mosquitto broker** add-on and the **MQTT integration**. Broker credentials are read from the Supervisor; there is nothing to enter.

## Google OAuth client

The add-on uses your own OAuth client — there is no shared cloud project and no quota shared with anyone else.

1. In [Google Cloud Console](https://console.cloud.google.com/apis/credentials), create a project and enable the **YouTube Data API v3**.
2. Configure the **OAuth consent screen** (*Google Auth Platform → Audience* in newer consoles). User type **External** is fine. While the app is in **Testing**, only accounts listed under **Test users** may sign in and refresh tokens expire after **7 days** — add the channel's Google account there to get started, then press **Publish app** once it works so the token stops expiring. Publishing a sensitive-scope app without verification just adds a "Google hasn't verified this app" interstitial (*Advanced → Go to … (unsafe)*) on the consent screen; verification itself is not needed for a private single-channel tool.
3. Create an **OAuth client ID** of type **Desktop app** and copy the client ID and secret into the add-on options. Nothing needs registering for this type: Google permits its `http://localhost` redirect out of the box, and Google's policy rejects plain-`http` redirects to anything *but* localhost — which is why a LAN address such as `http://homeassistant.local:8098/…` cannot be used.
4. Restart the add-on, open its web UI and press **Connect Google account**. If the Google account manages more than one channel (Brand Accounts), Google shows a chooser — **pick the channel**, not the account: the connection acts on exactly that channel and only its broadcasts appear. Consent to *Manage your YouTube account* (`youtube.force-ssl`).
5. Google then sends the browser to `http://localhost:8098/oauth/callback?…`, which shows a "can't connect" page unless that browser is running on the Home Assistant machine. That is expected — copy the whole URL from the address bar and paste it into the form on the web UI. The code in it completes the connection.

If you have a public **https** hostname that forwards to the add-on's port 8098 (a reverse proxy or tunnel), set `external_url` to it, use a **Web application** client instead, and register `<external_url>/oauth/callback` as its authorized redirect URI; the redirect then completes on its own.

**Which channel?** A channel only appears on Google's chooser if your Google account is an **owner or manager of its Brand Account** (listed at myaccount.google.com/brandaccounts). Access granted through *YouTube Studio → Settings → Permissions* is Studio-only and invisible to the API; the owner must add you as a Brand Account manager or connect the add-on themselves.

The refresh token is stored in the add-on's database (`/data/ylc.sqlite`) and survives restarts and updates. If Google revokes it the **Authorization** sensor flips to `unauthorized` and the web UI asks you to reconnect.

## Configuration

```yaml
google_client_id: "1234-abc.apps.googleusercontent.com"
google_client_secret: "GOCSPX-…"
external_url: ""
list_poll_minutes: 5
fast_poll_seconds: 3
fast_mode_minutes: 5
live_poll_seconds: 60
idle_poll_minutes: 10
log_level: info
```

| Option | Default | Meaning |
| --- | --- | --- |
| `google_client_id` / `google_client_secret` | required | Your OAuth client. |
| `external_url` | unset | Public **https** base URL that forwards to the add-on's port 8098. When set, the OAuth redirect is `<external_url>/oauth/callback` and completes automatically; when unset the redirect is `http://localhost:8098/oauth/callback` and you paste the result. |
| `list_poll_minutes` | `5` | 1–60. How often the broadcast list is re-read. |
| `fast_poll_seconds` | `3` | 1–30. Cadence while the fast-refresh window is armed. |
| `fast_mode_minutes` | `5` | 1–60. How long each armed fast-refresh window lasts. |
| `live_poll_seconds` | `60` | 15–600. Cadence while the selected broadcast is live. |
| `idle_poll_minutes` | `10` | 1–60. Baseline cadence while a broadcast is selected but idle. |
| `log_level` | `info` | `debug` / `info` / `warn` / `error` |

Configuration lives here; the MQTT devices are for scheduling and operating; presets, descriptions, thumbnails and off-pattern dates live in the web UI.

## The web UI (prepare)

Open it from the add-on's Info tab or the sidebar. It is restricted to Home Assistant admins and is dark-themed only.

**Presets** — a preset is everything a regular service needs, so scheduling is "pick preset, confirm date":

| Field | Meaning |
| --- | --- |
| Name | e.g. *Sunday morning* |
| Title | The broadcast title; `{date}` becomes the scheduled date, e.g. `Sunday Service – {date}` → *Sunday Service – 5 Jan 2025*. |
| Description | Copied to each broadcast. |
| Usual day & time | Pre-fills Date and Time with the next occurrence that isn't already taken, e.g. Sunday 09:30. |
| Privacy | public / unlisted / private. |
| Stream key | Which of the channel's stream keys (YouTube Studio → *Stream settings*) the broadcast is bound to — the one OBS is configured with. A broadcast without a stream key can never go live. |
| Thumbnail | JPEG/PNG up to 2 MB, uploaded to every broadcast scheduled from the preset. |

**Duplicate** copies a preset (with its thumbnail) as *<name> (copy)* and opens it for editing — the quick way to make a variant such as an evening service. Presets live in `/data/ylc.sqlite`; their images in `/data/images/`. Home Assistant's add-on backups include both.

**Broadcasts** — lists upcoming and live broadcasts (with the one currently on the Home Assistant panel marked), and *Never started* ones — scheduled more than a day ago and still `ready`, hidden from the panel, with a **Delete** button to clean them up.

- **Schedule a new stream** (the same thing the dashboard's Schedule button does, with a full form): choose a preset → the form is pre-filled → adjust anything, including an off-pattern date → **Schedule**. The add-on creates the broadcast with `enableAutoStart`/`enableAutoStop` off, binds the stream key, uploads the thumbnail and reads it back. It does not change the panel's selection.
- **Edit** any broadcast: title, description, date-time, privacy, stream key (fix a Studio-made broadcast that shows *no stream key*), replace the thumbnail — or **Delete** it (asks for confirmation; a live broadcast must be ended first).

Every action goes through the same verified write → read back path as the panel, so the Home Assistant entities update the moment YouTube confirms.

## The panel (manage & operate)

Two MQTT devices. Home Assistant's auto-generated dashboard gives each its own card; the device pages are single-purpose.

### Device: YouTube Live — the selected broadcast

| Entity | Type | Behaviour |
| --- | --- | --- |
| **Broadcast** | select | Upcoming and live broadcasts, live first then soonest first — so the right one is the first option. **Nothing selects automatically**: after a restart or after End Stream the panel reads *No broadcast selected* until someone picks. Attributes: `id`, `scheduled_start`, `privacy`, `lifecycle`, `thumbnail_url`, `watch_url`. |
| **Title** | text | Written to YouTube on Enter; the field only changes once YouTube confirms. |
| **Privacy** | select | public / unlisted / private, written immediately. |
| **Stage** | sensor (enum) | The one status line: `no_broadcast`, `no_stream_key`, `waiting_for_encoder`, `ready_to_go_live`, `starting`, `live`, `stream_stopping`, `ready_to_end`, `ending`, `ended`. |
| **Scheduled start** | sensor (timestamp) | Rendered relatively by Home Assistant: *in 3 days*, *in 20 minutes*. |
| **Thumbnail** | image | The broadcast's thumbnail, for `picture-entity` cards. |
| **Live** | binary sensor (running) | On while on air (`live`, `stream_stopping`, `ready_to_end`). For automations: ON AIR light, notify, dim the foyer TV. |
| **Encoder connected** | binary sensor (connectivity) | On while YouTube is receiving the encoder's stream. |
| **Go Live** | button | Available only in `ready_to_go_live`. |
| **End Stream** | button | Available only in `ready_to_end`. |
| **Fast refresh** / **Fast refresh remaining** | switch / sensor | The fast-poll window (see *Quota*): auto-armed by any interaction, re-armable and cancellable by hand; the add-on switches it off when the window expires. |
| Broadcast status · Stream health · Channel · Authorization | sensors (diagnostic) | Raw YouTube lifecycle, raw ingestion/health, connected channel, `authorized` / `unauthorized`. |

### Device: YouTube Live Scheduling — create from a preset

| Entity | Type | Behaviour |
| --- | --- | --- |
| **Preset** | select | Your presets by name; remembers the last one used. Choosing one resets **Date** and **Time** to its **next usual slot that isn't already taken** (if this Sunday 09:30 already has a stream, the Sunday after is offered). |
| **Date** / **Time** | date · time | Home Assistant's native date and time pickers (MQTT `date` and `time` entities, Home Assistant 2026.5 or newer). |
| **Schedule** | button | Creates the broadcast at Date + Time with the preset's title (date substituted), description, privacy, stream key and thumbnail. Greyed while no preset is chosen, the preset has no stream key, or Date + Time is in the past. It does **not** change which broadcast the panel is on — pick it from **Broadcast** when you want it. |

Invalid input (an empty title, a title over 100 characters, an unknown option) is rejected and the field snaps back.

### Dashboard cards

Core Lovelace only — no HACS, no custom cards. Paste into the dashboard editor's YAML mode.

**Volunteer card** — thumbnail and title, when it starts, the stage, and only the button that is currently valid:

```yaml
type: vertical-stack
cards:
  - type: picture-entity
    entity: image.youtube_live_control_thumbnail
    name: Stream
    show_state: false
    show_name: false
  - type: entities
    entities:
      - entity: select.youtube_live_control_broadcast
      - entity: sensor.youtube_live_control_scheduled_start
      - entity: sensor.youtube_live_control_stage
      - entity: binary_sensor.youtube_live_control_encoder
  - type: conditional
    conditions:
      - condition: state
        entity: sensor.youtube_live_control_stage
        state: ready_to_go_live
    card:
      type: button
      entity: button.youtube_live_control_go_live
      name: Go Live
      icon: mdi:play-circle
  - type: conditional
    conditions:
      - condition: state
        entity: sensor.youtube_live_control_stage
        state: ready_to_end
    card:
      type: button
      entity: button.youtube_live_control_end_stream
      name: End Stream
      icon: mdi:stop-circle
  - type: entities
    entities:
      - entity: switch.youtube_live_control_fast_mode
```

The picture is the **Thumbnail** image entity, which is unavailable (and the card blank) while the selected broadcast has no thumbnail.

**Producer card** — schedule, then manage:

```yaml
type: vertical-stack
cards:
  - type: entities
    title: Schedule
    entities:
      - entity: select.youtube_live_scheduling_preset
      - entity: date.youtube_live_scheduling_date
      - entity: time.youtube_live_scheduling_time
      - entity: button.youtube_live_scheduling_schedule
  - type: entities
    title: Selected broadcast
    entities:
      - entity: select.youtube_live_control_broadcast
      - entity: text.youtube_live_control_title
      - entity: select.youtube_live_control_privacy
      - entity: sensor.youtube_live_control_scheduled_start
      - entity: sensor.youtube_live_control_stage
```

## Going live, safely

Everything is **verify → write → read back**; the entities only move when YouTube confirms. State is retained, commands are not, and command replays from the broker are ignored — restarts never start or stop a broadcast.

- Broadcasts are created and saved with `enableAutoStart` and `enableAutoStop` explicitly **false**. OBS starting or stopping never transitions the broadcast; only the buttons do.
- **Go Live** is available only in stage `ready_to_go_live`: the bound stream's `streamStatus` is `active`, i.e. OBS is actually sending. Start OBS, watch **Stage** go from `waiting_for_encoder` to `ready_to_go_live` (and **Encoder connected** turn on), then press it.
- **End Stream** is available only in stage `ready_to_end`: live **and** YouTube confirms the stream has stopped. Stop OBS first; YouTube's `streamStatus` lags by up to a minute, during which **Stage** shows `stream_stopping` and the button stays unavailable.
- Broadcasts created in YouTube Studio with a monitor stream are taken through `testing` automatically on the way to live; broadcasts scheduled by the add-on disable the monitor stream and go live in one step.
- The gate is re-verified against the API at the moment a button is pressed, not just against the last poll.

## Quota and tiered polling

The YouTube Data API allows 10,000 units/day by default. Reads cost 1; insert/update/bind/transition/thumbnail cost 50 each. Status polling is driven by human presence, not stream state, in three tiers (and only runs while a broadcast is selected):

| Tier | When | Default cadence | Cost |
| --- | --- | --- | --- |
| Idle | selected, nothing happening | 10 min | ~288 units/day |
| Live | broadcast is on air | 60 s | ~180 units/hour |
| Fast | **Fast refresh** armed | 3 s | ~40 units/minute, window capped at 5 min |

The fast window is armed automatically by **any** interaction with the panel — changing the selection, typing a title, pressing a button (even a refused press: tapping End Stream while YouTube still reports the stream active is exactly the moment you want a fast poll). Each interaction restarts the timer, and the add-on switches it off itself when the window expires — so a dashboard left on Sunday's selection cannot drain Monday's quota. It also self-arms when a Go Live / End Stream transition finishes and when authorization is granted.

Budgeting: the separate list poll costs 2 units per cycle (~576/day at 5 minutes); a full service — schedule, a couple of edits, go live, end, with generous fast-mode use — stays around 500–800 units.

YouTube does not expose remaining quota through its API; actual usage is visible in Google Cloud Console under **APIs & Services → YouTube Data API v3 → Quotas**.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Entities unavailable, Authorization `unauthorized` | Consent hasn't been given or was revoked. Open the web UI and connect. |
| Broadcast select has no options although a stream is scheduled | The connection is to a different channel than the one holding the broadcast — check the **Channel** sensor / the web UI header. Reconnect and pick the right channel on Google's chooser. Also check the web UI's *Never started* section: a broadcast scheduled more than a day ago is hidden from the panel. |
| Google's chooser doesn't list the channel | Your account isn't a manager of that channel's Brand Account (only Studio permissions). See *Which channel?* above. |
| `Error 400: invalid_request` / "doesn't comply with Google's OAuth 2.0 policy" | The redirect URI is plain `http` to a non-localhost address. Leave `external_url` unset (localhost redirect + paste), or set it to an **https** URL. |
| `redirect_uri_mismatch` from Google | With `external_url` set, the OAuth client must be a **Web application** with exactly `<external_url>/oauth/callback` registered. With it unset, use a **Desktop app** client. |
| Browser shows "can't connect" to `localhost:8098` after consenting | Expected when `external_url` is unset — the code is in the address bar. Copy the whole URL and paste it into the form on the web UI. |
| "Access blocked: … has not completed the Google verification process" | The consent screen is in **Testing** and the signing-in account isn't a test user. Add it under *Test users*, or **Publish app**. |
| "Google hasn't verified this app" warning | Expected for a published, unverified app. *Advanced → Go to … (unsafe)* continues. |
| Go Live stays unavailable | Read **Stage**: `waiting_for_encoder` means OBS isn't streaming yet; `no_stream_key` means the broadcast has no stream key — fix it on the broadcast's edit page in the web UI. |
| End Stream stays unavailable after stopping OBS | Stage `stream_stopping` is expected for up to a minute — `streamStatus` lags. Tap End Stream once (or flip **Fast refresh** on): the refused press arms the fast poll and the button enables as soon as Stage reaches `ready_to_end`. |
| Schedule button is greyed | No preset chosen, the preset has no stream key (edit it in the web UI), or Date + Time is in the past. |
| No **Date** / **Time** entities on the scheduling device | The MQTT date and time entities need Home Assistant 2026.5 or newer. |
| Sensors feel stale | The idle tier polls every 10 minutes. Touch anything on the panel or switch **Fast refresh** on for the 3-second cadence. |
| `authorization_revoked` in log, Authorization `unauthorized` every week | The consent screen is still in **Testing**, where refresh tokens expire after 7 days. Press **Publish app** on the consent screen, then reconnect once. |
| `quotaExceeded` errors | Daily quota exhausted; it resets at midnight Pacific. Raise the poll intervals. |
| `supervisor did not return a usable MQTT service` | Install/start the Mosquitto broker add-on. |
| `mqtt_disconnected` / `mqtt_connect_error` | Broker is down; the add-on reconnects and republishes on its own. |
