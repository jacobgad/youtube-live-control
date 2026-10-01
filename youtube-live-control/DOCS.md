# YouTube Live Control

Runs a channel's scheduled YouTube live streams from Home Assistant.

- **Producers** schedule from a preset on the dashboard: preset → date → time → **Schedule**. Presets, descriptions, thumbnails and one-off edits live in the add-on's web UI.
- **Volunteers** operate on the day: pick the stream (it is the first option), fix the title if needed, **Go Live** once OBS is sending, **End Stream** after the service. One **Stage** sensor says what to do next.

## Requirements

- Home Assistant 2026.5 or newer (MQTT date-time entity)
- The **Mosquitto broker** add-on and the **MQTT integration** (credentials are read from the Supervisor)
- A Google Cloud project with the **YouTube Data API v3** enabled and an OAuth client (below)

## Setup

### 1. Google OAuth client

1. In [Google Cloud Console](https://console.cloud.google.com/apis/credentials) create a project and enable the **YouTube Data API v3**.
2. Configure the **OAuth consent screen** (*Google Auth Platform → Audience*). User type **External**. Add the channel's Google account under **Test users**, then — once connected and working — press **Publish app**: in *Testing* Google expires the refresh token every 7 days. Publishing an unverified app only adds a "Google hasn't verified this app" interstitial; verification is not needed for a private tool.
3. Create an **OAuth client ID** of type **Desktop app** and copy its ID and secret into the add-on options. Nothing else needs registering: Google's policy only allows plain-`http` redirects to `localhost`, which a Desktop client permits by default.

### 2. Connect

1. Start the add-on and open its web UI (**Open Web UI** on the add-on page, or **YouTube Live** in the sidebar).
2. Press **Connect**. If the account manages several channels, Google shows a chooser — **pick the channel**, not the account. Only that channel's broadcasts are visible to the add-on.
3. Consent to *Manage your YouTube account*. Google then sends the browser to `http://localhost:8098/…`, which fails to load unless the browser runs on the Home Assistant machine — expected. Copy the full URL from the address bar and paste it into the form on the web UI.

The web UI header shows the connected channel; the **Channel** sensor shows the same.

A channel appears on Google's chooser only if the account is an owner or manager of its **Brand Account** (myaccount.google.com/brandaccounts). Access granted through *YouTube Studio → Settings → Permissions* is Studio-only; the owner must add you as a Brand Account manager or connect themselves.

If you have a public **https** hostname forwarding to the add-on's port 8098, set **OAuth redirect base URL**, use a **Web application** client with `<url>/oauth/callback` registered, and the redirect completes on its own.

### 3. Presets

Web UI → **Presets → Create**. A preset is everything a regular service needs:

| Field | Meaning |
| --- | --- |
| Name | e.g. *Sunday morning* |
| Title | `{date}` becomes the scheduled date: `Sunday Service – {date}` → *Sunday Service – 5 Jan 2025* |
| Description | copied to each broadcast |
| Usual day and time | pre-fills Start with the next occurrence that isn't already scheduled |
| Privacy | public / unlisted / private |
| Category | one of the 15 categories YouTube Studio offers; *YouTube default* leaves it to YouTube |
| Stream key | the channel's stream key OBS is configured with; a broadcast without one can never go live |
| Thumbnail | from the **Images** library or uploaded here |

**Duplicate** copies a preset as *<name> (copy)* for variants such as an evening service.

## Using it

### Schedule (dashboard, *YouTube Live Scheduling* device)

Pick a **Preset** — **Start** jumps to its next free usual slot and **Privacy** to the preset's — adjust either if this stream differs, press **Schedule**. The broadcast is created with the preset's title, description, category, stream key and thumbnail and appears in the **Broadcast** select; the fields clear to confirm. Schedule is greyed while no preset is chosen, the preset has no stream key, or the slot is in the past. It never changes which broadcast Home Assistant is on.

### Operate (dashboard, *YouTube Live* device)

1. Pick the stream in **Broadcast** (sorted live-first, then soonest — the right one is first). Nothing is ever selected for you.
2. Edit **Title** or **Privacy** if needed; each change is written to YouTube immediately and the field updates once YouTube confirms.
3. Start OBS. **Stage** goes `waiting_for_encoder` → `ready_to_go_live`; press **Go Live**.
4. After the service stop OBS. **Stage** shows `stream_stopping` while YouTube catches up (up to a minute), then `ready_to_end`; press **End Stream**.

| Stage | Meaning |
| --- | --- |
| `no_broadcast` | nothing selected |
| `no_stream_key` | the broadcast has no stream key — fix it in the web UI |
| `waiting_for_encoder` | scheduled; OBS is not sending |
| `ready_to_go_live` | YouTube is receiving the encoder — **Go Live** available |
| `starting` / `ending` | transition in progress |
| `live` | on air |
| `stream_stopping` | OBS stopped; YouTube has not registered it yet |
| `ready_to_end` | stream stopped — **End Stream** available |
| `ended` | complete |

### Web UI

| Tab | Purpose |
| --- | --- |
| Broadcasts | schedule from a preset with a full form (any date, off-pattern services); edit title, description, date and time, privacy, category, stream key, thumbnail; delete. *Never started* lists broadcasts scheduled more than a day ago that never went live — hidden from Home Assistant — for cleanup. |
| Presets | create, edit, duplicate; delete from a preset's page |
| Images | the thumbnail library: upload once, pick anywhere; delete once no preset uses it |
| Connection | Google account and channel |

The web UI is for Home Assistant admins only.

## Entities

### YouTube Live — the selected broadcast

| Entity | Type | Notes |
| --- | --- | --- |
| Broadcast | select | attributes: `id`, `scheduled_start`, `privacy`, `lifecycle`, `thumbnail_url`, `watch_url` |
| Title | text | applied on Enter |
| Privacy | select | applied on change |
| Stage | sensor (enum) | see table above |
| Scheduled start | sensor (timestamp) | Home Assistant renders it relatively (*in 20 minutes*) |
| Thumbnail | image | unavailable while the broadcast has none |
| Live | binary sensor (running) | on for `live`, `stream_stopping`, `ready_to_end` — for ON AIR lights and notifications |
| Encoder connected | binary sensor (connectivity) | on while YouTube is receiving the stream |
| Go Live / End Stream | buttons | available only in `ready_to_go_live` / `ready_to_end` |
| Delete | button | removes the selected broadcast from YouTube; unavailable while it is live or transitioning |
| Fast refresh / Fast refresh remaining | switch / sensor | the fast-poll window (below) |
| Broadcast status, Stream health, Channel, Authorization | sensors (diagnostic) | raw YouTube values, connected channel, `authorized` / `unauthorized` |

### YouTube Live Scheduling

| Entity | Type |
| --- | --- |
| Preset | select (empty until chosen) |
| Start | datetime |
| Privacy | select — pre-filled from the preset, overridable for this stream |
| Schedule | button |

Both devices appear under **Settings → Devices & services → MQTT**; Home Assistant's auto-generated dashboard gives each its own card. Invalid input (empty title, over 100 characters, unknown option) is rejected and the field snaps back.

## Safety

Every write is verified against YouTube first and read back before Home Assistant is updated. State is retained on MQTT, commands are not, and replayed commands are ignored, so restarts never start or stop a stream. While a change is in flight to YouTube, every input on both devices (selects, fields, buttons, switch) is unavailable and comes back together once the readback lands — after Schedule, the Broadcast select reappears only once the new stream is one of its options. The sensors keep reporting throughout.

- Broadcasts are written with `enableAutoStart` and `enableAutoStop` **false**: OBS starting or stopping never transitions a broadcast; only the buttons do.
- **Go Live** requires YouTube to report the stream `active`. **End Stream** requires the stream to have stopped, which YouTube reports up to a minute after OBS stops — the `stream_stopping` stage.
- Both gates are re-checked at the moment a button is pressed.
- Editing a broadcast preserves the settings the add-on does not manage (DVR, latency, embedding, captions).

## Polling

YouTube's API has no push, so the add-on polls — only while a broadcast is selected — in three tiers driven by human presence:

| Tier | When | Cadence |
| --- | --- | --- |
| Idle | nothing happening | **Idle refresh** (also refreshes the broadcast list) |
| Live | broadcast on air | **Live refresh** |
| Fast | **Fast refresh** window | **Fast refresh** cadence for **Fast refresh duration** |

Any interaction with the Home Assistant entities arms the fast window — including a refused button press, which is exactly when a fast answer is wanted. The add-on switches it off when the window expires; the switch is there to arm it by hand.

Reads cost 1 quota unit and writes 50 against the API's 10,000/day default. At the defaults a service week uses well under a tenth of it. Actual usage is shown in Google Cloud Console under **APIs & Services → YouTube Data API v3 → Quotas**.

## Options

Each option is described on the add-on's Configuration tab.

| Option | Default | Meaning |
| --- | --- | --- |
| Google client ID / secret | — | the OAuth client |
| OAuth redirect base URL | unset | https base URL forwarding to port 8098; the redirect becomes `<url>/oauth/callback` |
| Idle refresh | 600 s | list and selected-broadcast checks while nothing is happening (60–3600) |
| Live refresh | 60 s | selected-broadcast checks while on air (15–600) |
| Fast refresh | 3 s | cadence inside a fast-refresh window (1–30) |
| Fast refresh duration | 300 s | how long the window stays on after an interaction (60–3600) |
| Log level | info | debug / info / warn / error |

## Storage

`/data/ylc.sqlite` holds presets, image records, settings and the Google refresh token; `/data/images/` holds thumbnail files. Home Assistant's add-on backups include both.

## Troubleshooting

| Symptom | Cause / fix |
| --- | --- |
| Entities unavailable; Authorization `unauthorized` | not connected, or Google revoked the token — connect in the web UI |
| Authorization drops to `unauthorized` weekly | the consent screen is in *Testing*; press **Publish app** and reconnect once |
| "Access blocked: … has not completed the Google verification process" | the account is not a test user; add it, or publish the app |
| `Error 400: invalid_request` from Google | OAuth redirect base URL is a plain-http or LAN address; leave it empty or use https |
| `redirect_uri_mismatch` | with a redirect base URL: Web client must register `<url>/oauth/callback`; without: use a Desktop client |
| Browser cannot reach `localhost:8098` after consent | expected — paste the URL into the web UI |
| Google's chooser does not list the channel | the account is not a Brand Account manager of it |
| Broadcast select is empty although a stream is scheduled | wrong channel (check **Channel**), or the broadcast is under *Never started* in the web UI |
| Go Live unavailable | read **Stage**: `waiting_for_encoder` — OBS not sending; `no_stream_key` — fix in the web UI |
| End Stream unavailable after stopping OBS | `stream_stopping` for up to a minute; a tap on End Stream arms fast polling |
| Schedule greyed | no preset, preset without stream key, or a start in the past |
| Start entity missing | Home Assistant older than 2026.5 |
| `quotaExceeded` in the log | daily quota spent; resets midnight Pacific; raise the poll intervals |
| `supervisor did not return a usable MQTT service` | start the Mosquitto broker add-on |
