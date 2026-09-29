# YouTube Live Control

Runs a channel's scheduled YouTube live broadcasts from Home Assistant. The intended workflow, on an iPad dashboard: pick (or create) the Sunday broadcast, fix the title, press **Go Live** once OBS is streaming, press **End Stream** after the service.

## Installation

1. **Settings → Add-ons → Add-on Store → ⋮ → Repositories**, add this repository's URL.
2. Install **YouTube Live Control**.
3. Create a Google OAuth client (below), enter it on the **Configuration** tab, **Start** the add-on.
4. Open the add-on's web UI (sidebar **YouTube Live**) and connect the channel's Google account once.
5. Entities appear under **Settings → Devices & services → MQTT** as one **YouTube Live** device.

Requires the **Mosquitto broker** add-on and the **MQTT integration**. Broker credentials are read from the Supervisor; there is nothing to enter.

## Google OAuth client

The add-on uses your own OAuth client — there is no shared cloud project and no quota shared with anyone else.

1. In [Google Cloud Console](https://console.cloud.google.com/apis/credentials), create a project and enable the **YouTube Data API v3**.
2. Configure the **OAuth consent screen** (External is fine; add the channel's Google account as a test user, or publish the app so the refresh token doesn't expire after 7 days).
3. Create an **OAuth client ID** of type **Web application** and copy the client ID and secret into the add-on options.
4. Add the **authorized redirect URI** the web UI shows you — by default `http://<your-ha-host>:8098/oauth/callback` (port 8098 is exposed by the add-on for exactly this).
5. In the web UI press **Connect Google account** and consent to *Manage your YouTube account* (`youtube.force-ssl`). If the redirect page cannot load, paste the URL from the address bar into the fallback form; the code is in it.

The refresh token is stored in `/data/token.json` and survives restarts and updates. Consent is needed once; if Google ever revokes the token the **Authorization** sensor flips to `unauthorized` and the web UI asks you to reconnect.

## Configuration

```yaml
google_client_id: "1234-abc.apps.googleusercontent.com"
google_client_secret: "GOCSPX-…"
external_url: ""
privacy: public
thumbnails_dir: /media/youtube-live-control
log_level: info
```

| Option | Default | Meaning |
| --- | --- | --- |
| `google_client_id` / `google_client_secret` | required | Your OAuth client. |
| `external_url` | derived | Base URL for the OAuth redirect when the auto-detected host is wrong, e.g. `http://192.168.1.10`. `/oauth/callback` and port 8098 are appended/expected. |
| `privacy` | `public` | Privacy of broadcasts created by **Create** (`public` / `unlisted` / `private`). |
| `thumbnails_dir` | `/media/youtube-live-control` | Folder of `.jpg`/`.png` files offered by the **Thumbnail** select. |
| `log_level` | `info` | `debug` / `info` / `warn` / `error` |

The options hold only infrastructure that needs a restart. Everything behavioural — poll cadences and the fast-refresh window — is a number entity on the device (under *Configuration*), adjustable at runtime and persisted in `/data/settings.json`.

### Settings entities

| Entity | Default | Range | Meaning |
| --- | --- | --- | --- |
| **List poll interval** | 5 min | 1–60 | Broadcast list and thumbnail folder refresh. |
| **Fast poll interval** | 3 s | 1–30 | Cadence while the fast-refresh window is armed. |
| **Fast refresh window** | 5 min | 1–60 | How long each armed window lasts. |
| **Live poll interval** | 60 s | 15–600 | Cadence while the selected broadcast is live. |
| **Idle poll interval** | 10 min | 1–60 | Baseline cadence while a broadcast is selected but idle. |

Changes apply immediately and survive restarts. Values are validated against the ranges above; out-of-range input snaps back.

## The panel

One MQTT device, **YouTube Live** — a selector plus a detail panel, not one entity per broadcast:

| Entity | Type | Behaviour |
| --- | --- | --- |
| **Broadcast** | select | Upcoming (and currently live) broadcasts plus **New stream…**. Changing it republishes every detail entity, so the fields below pre-fill. |
| **Title** | text | Draft title; applied by **Save** / **Create**. |
| **Scheduled start** | text | `YYYY-MM-DD HH:MM` in Home Assistant's timezone (`T` separator and full RFC 3339 also accepted). MQTT has no datetime platform, hence a validated text field. |
| **Thumbnail** | select | **Keep current** or a file from `thumbnails_dir`; uploaded on **Save** / **Create**. |
| **Fast refresh** | switch | The fast-poll window: auto-armed by any panel interaction, re-armable and cancellable by hand. The add-on turns it off itself when the window expires. |
| **Fast refresh remaining** | sensor | Minutes left in the armed window; 0 while off. |
| **Stream health** | sensor | The bound stream's ingestion state (`inactive` / `ready` / `created` / `error`) until it is receiving, then YouTube's health verdict (`good` / `ok` / `bad` / `noData`); `no stream bound` when there is none. |
| **Broadcast status** | sensor | `ready`, `live`, `complete`, … plus `starting`, `ending` and `live (waiting for stream to stop)`. |
| **Viewers** | sensor | Concurrent viewers while live, else 0. |
| **Save** | button | Writes Title / Scheduled start / Thumbnail to the selected broadcast. |
| **Create** | button | Creates a scheduled broadcast from the drafts (only with **New stream…** selected), binds it to the channel's stream key. An empty start rounds up to the next quarter hour (3:31 → 3:45). |
| **Go Live** | button | Transitions to live. |
| **End Stream** | button | Transitions to complete. |
| **Authorization** | sensor (diagnostic) | `authorized` / `unauthorized`. |

Invalid input (a malformed date, a title over 100 characters, an unknown option) is rejected and the field snaps back to the retained state.

## Going live, safely

Everything is **verify → write → read back**; the entities only move when YouTube confirms. State is retained, commands are not, and command replays from the broker are ignored — restarts never start or stop a broadcast.

- Broadcasts are created and saved with `enableAutoStart` and `enableAutoStop` explicitly **false**. OBS starting or stopping never transitions the broadcast; only the buttons do.
- **Go Live** is available only while the bound stream's `streamStatus` is `active` — i.e. OBS is actually sending. Start OBS, watch **Stream health** switch from `inactive` to `good`, then press it.
- **End Stream** is available only while live **and** after the stream has stopped. Stop OBS first; YouTube's `streamStatus` lags by up to a minute, during which **Broadcast status** shows `live (waiting for stream to stop)` and the button stays unavailable. When the status catches up, the button becomes available.
- Broadcasts created in YouTube Studio with a monitor stream are taken through `testing` automatically on the way to live; broadcasts created by the add-on disable the monitor stream and go live in one step.
- The gate is re-verified against the API at the moment a button is pressed, not just against the last poll.

## Quota and tiered polling

The YouTube Data API allows 10,000 units/day by default. Reads cost 1; insert/update/bind/transition/thumbnail cost 50 each. Status polling is driven by human presence, not stream state, in three tiers (and only runs while a broadcast is selected):

| Tier | When | Default cadence | Cost |
| --- | --- | --- | --- |
| Idle | selected, nothing happening | 10 min | ~288 units/day |
| Live | broadcast is on air | 60 s | ~180 units/hour |
| Fast | **Fast refresh** armed | 3 s | ~40 units/minute, window capped at 5 min |

The fast window is armed automatically by **any** interaction with the panel — changing the selection, typing a title, pressing a button (even a refused press: tapping End Stream while YouTube still reports the stream active is exactly the moment you want a fast poll). Each interaction restarts the timer, and the add-on switches it off itself when the window expires — so a dashboard left on Sunday's selection cannot drain Monday's quota. It also self-arms when a Go Live / End Stream transition finishes and when authorization is granted, so the sensors settle without another tap. The settings numbers are the one exception: changing a poll interval is admin work and does not arm it.

Budgeting: the separate list poll costs 2 units per cycle (~576/day at 5 minutes); a full service — create, a couple of saves, thumbnail, go live, end, with generous fast-mode use — stays around 500–800 units, comfortably inside a 30% polling budget. All cadences are adjustable via the settings entities.

## Thumbnails

Put `.jpg`/`.png` files (max 2 MB, ideally 1280×720) into `thumbnails_dir` (default `/media/youtube-live-control`, visible in Home Assistant under **Media**). The folder is re-scanned on the list poll. Pick a file and press **Save** (or **Create**); the select then returns to **Keep current**.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Entities unavailable, Authorization `unauthorized` | Consent hasn't been given or was revoked. Open the web UI and connect. |
| `redirect_uri_mismatch` from Google | The redirect URI on the OAuth client must exactly match the one shown in the web UI. Set `external_url` if the auto-detected host is wrong. |
| Google page says the redirect failed to load | Port 8098 isn't reachable from the browser. The code is still in the address bar — paste the full URL into the web UI's fallback form. |
| `access_denied` / app not verified | Add the account as a test user on the consent screen, or publish the app. |
| Go Live stays unavailable | The stream isn't `active`: OBS isn't streaming, or the broadcast has no bound stream (`Stream health: no stream bound`). Selecting the broadcast and pressing Create/Save re-binds only on create; bind Studio-made broadcasts to your stream key in Studio. |
| End Stream stays unavailable after stopping OBS | Expected for up to a minute — `streamStatus` lags. Tap End Stream once (or flip **Fast refresh** on): the refused press arms the fast poll and the button enables as soon as YouTube reports the stream stopped. |
| Sensors feel stale | The idle tier polls every 10 minutes. Touch anything on the panel or switch **Fast refresh** on for the 3-second cadence. |
| `authorization_revoked` in log | Google invalidated the refresh token (revoked access, password change, or 7-day test-mode expiry). Reconnect via the web UI. |
| `quotaExceeded` errors | Daily quota exhausted; it resets at midnight Pacific. Raise the poll intervals. |
| `supervisor did not return a usable MQTT service` | Install/start the Mosquitto broker add-on. |
| `mqtt_disconnected` / `mqtt_connect_error` | Broker is down; the add-on reconnects and republishes on its own. |
