# Changelog

## 1.9.2

- End Stream now clears the panel immediately on completion, matching Schedule and Delete, instead of leaving the finished broadcast selected until the next list poll.

## 1.9.1

- Fix: the Preset select did not clear after Schedule, and the Broadcast select kept a stale selection after End Stream or Delete. "Nothing selected" was published as a placeholder label that is not one of the options, which Home Assistant ignores; it is now published as `None`. Re-picking the apparently still-selected preset sent no command, which is why Start and Privacy then stayed empty.

## 1.9.0

- **One in-flight lock.** While any change is being written to YouTube — Schedule, Go Live, End Stream, Delete, Title or Privacy, or the same actions from the web UI — every input on both devices is unavailable and returns together once the readback lands, so after Schedule the Broadcast select reappears with the new stream already in it. Sensors stay live. Replaces the per-button busy flags; a Title or Privacy edit made while another change is in flight is dropped and the field snaps back on readback.

## 1.8.0

- Scheduling device gains **Privacy**, pre-filled from the preset and overridable per stream; cleared with the rest after Schedule.
- **Delete** button on the selected-broadcast device, unavailable while the broadcast is live or transitioning.
- Dead fields removed (`Stream.IsReusable`, `Stream.Status`, `Image.CreatedAt`); `liveStreams.list` asks for fewer parts.

## 1.7.1

- Double-tap protection: one in-flight instance per command. While Go Live, End Stream or Schedule runs, its button is unavailable in Home Assistant; a second press is ignored and logged. Web forms disable their buttons on submit, and a duplicate request is refused with "already in progress".

## 1.7.0

- Scheduling device: **Start** is one date-time entity again (Home Assistant's native picker), reversing 1.3.2's Date/Time split; the retired configs are cleared on start.

## 1.6.3

- Scheduling device clears Preset, Date and Time after a successful Schedule, as the visible confirmation; it starts empty (the last-used preset is no longer remembered).

## 1.6.2

- Category select shows exactly the 15 categories YouTube Studio offers, sorted by name. Regional API lists are unreliable (Australia's omits *Nonprofits & Activism* entirely), so the global assignable set is used.

## 1.6.1

- Category list no longer filters on the API's `assignable` flag, which wrongly hid *Nonprofits & Activism* for some regions; every category the API returns for the channel's country is offered.

## 1.6.0

**Options renamed — re-enter them after updating.** Five polling knobs become four, all in seconds: **Idle refresh** (`refresh_idle_seconds`, drives the broadcast list and the idle status check), **Live refresh** (`refresh_live_seconds`), **Fast refresh** (`refresh_fast_seconds`) and **Fast refresh duration** (`fast_refresh_duration_seconds`). `external_url` is now **OAuth redirect base URL** (`oauth_redirect_base_url`). Every option has a name and description on the Configuration tab.
- Presets and broadcasts gain a **Category** (YouTube video category, from the channel's country; one `videos.update` per schedule). Comment settings are not exposed by the YouTube API and remain a Studio setting.
- "on the panel" wording replaced with "Selected in Home Assistant".

## 1.5.1

- Web UI: one stylesheet for the whole site; consistent cards (status left, action right), one-word buttons, image gallery and picker tiles; connection page cut to the essentials; preset delete moved onto the preset's page; image names dropped (schema 3).

## 1.5.0

- **Image library** (web UI → Images): upload thumbnails once and pick them on preset and broadcast forms; each image shows how many presets use it and can be deleted once unused. Duplicating a preset now shares its image instead of copying the file. Schema migration 2 moves existing 1.4.0 preset thumbnails into the library.
- Documentation rewritten around setup, use and reference tables; the dashboard card YAML is gone (Home Assistant's auto-generated device cards cover both devices).

## 1.4.0

**Fresh setup required.** Storage moves to one SQLite database (`/data/ylc.sqlite`) plus one images directory (`/data/images/`): presets, the last-used preset and the Google refresh token are all in the database, with a versioned schema for future migrations. Nothing is imported from the 1.x JSON files — after updating, connect the Google account again and recreate presets. Backups now mean two paths.

## 1.3.2

- Scheduling device: **Date** and **Time** are two entities again — Home Assistant's native `date` and `time` pickers (MQTT platforms, 2026.5+) — because the combined `datetime` entity does not render well in cards. Still any minute; the retired `datetime` config is cleared on start.

## 1.3.1

- Web UI: **Duplicate** a preset (copies the thumbnail too) and land on the copy's edit page.

## 1.3.0

- **Native date-time picker for scheduling.** Home Assistant 2026.5 added an MQTT `datetime` platform, so the Date and Time selects are replaced by one **Start** entity — the platform's own picker — and the half-hour rule is gone everywhere (dashboard, presets, web UI). Requires Home Assistant 2026.5 or newer (declared in the add-on manifest). The retired Date/Time select configs are cleared on start.

## 1.2.1

- Web UI: **Delete** for broadcasts (edit page and the *Never started* list, with confirmation; live broadcasts must be ended first).
- Web UI is dark-only with one hard-coded palette; forms use a label / control / hint grid so hints no longer push neighbouring controls out of alignment.

## 1.2.0

Schedule from the dashboard, operate from the dashboard; set up in the web UI.

- **Two MQTT devices.** *YouTube Live* is the selected broadcast (manage & operate); *YouTube Live Scheduling* creates broadcasts from presets. Home Assistant's auto-generated dashboard gives each its own card.
- **Presets** (web UI → Presets): name, title pattern with `{date}`, description, privacy, **stream key** (chosen from the channel's `liveStreams`), thumbnail image, usual day and time. Stored in `/data/presets/`.
- **Scheduling device**: Preset (remembers the last used), Date (next four weeks), Time (half-hour steps), Schedule. Choosing a preset resets Date/Time to its next *free* usual slot; Schedule is greyed without a preset, without a stream key, or for a past slot. It never changes the panel's selection.
- **Stage** sensor (`device_class: enum`) folds YouTube's lifecycle and the encoder's ingestion state into one word — `waiting_for_encoder`, `ready_to_go_live`, `live`, `stream_stopping`, `ready_to_end`, … — and drives the Go Live / End Stream availability. **Scheduled start** is a timestamp sensor (*in 20 minutes*); **Live** and **Encoder connected** binary sensors for automations; a **Thumbnail** image entity for picture cards; the Broadcast select carries `thumbnail_url`, `watch_url` and more as attributes. All derived from data already polled — zero extra quota.
- **Panel edits apply immediately.** Title (on Enter) and the new **Privacy** select write straight to YouTube and only move on readback. **Save**, **Create**, **Scheduled start** (text), **Thumbnail**, **Viewers** and the *New stream…* pseudo-entry are gone; stale discovery configs are cleared on start. Dropping Viewers removes the `videos.list` call.
- **Nothing selects automatically.** The Broadcast select is sorted live-first then soonest-first so the right stream is the first option; after a restart or End Stream the panel reads *No broadcast selected* until someone picks.
- **Stale broadcasts hidden**: scheduled more than a day ago and never started → hidden from the panel, listed under *Never started* in the web UI. The channel's persistent default broadcast is excluded (`broadcastType=event`). This is what made the Broadcast select show more than the one scheduled stream.
- **Web UI**: Broadcasts tab (list, edit title/description/date-time/privacy/stream key/thumbnail, schedule from a preset with a full form), Presets tab, Connection tab. Half-hour times everywhere.
- Options `privacy` and `thumbnails_dir` removed; the `media`/`share` mounts are no longer needed.
- Volunteer and producer dashboard cards (core Lovelace only) in DOCS.md.

## 1.1.0

- **Channel selection for accounts that manage several YouTube channels.** Consent now always shows Google's account chooser (`prompt=select_account`), so a Brand Account channel can be picked explicitly; a scheduled stream that didn't appear in the Broadcast select was almost certainly on a different channel than the one connected. The connected channel is shown in the web UI and as a **Channel** diagnostic sensor, and logged as `channel_connected`.
- **Polling intervals move back to the add-on options** (`list_poll_minutes`, `fast_poll_seconds`, `fast_mode_minutes`, `live_poll_seconds`, `idle_poll_minutes`). The MQTT device is now purely for operating YouTube; the settings number entities and `/data/settings.json` are gone, and their stale discovery configs are cleared on start.
- `broadcast_list_refreshed` is logged at info level with upcoming/active counts.

## 1.0.2

- **Fix Google refusing consent with `Error 400: invalid_request` ("doesn't comply with Google's OAuth 2.0 policy").** Google only allows plain-`http` redirect URIs to localhost; the previous default pointed at the Home Assistant host's LAN address. The default redirect is now `http://localhost:8098/oauth/callback` with a **Desktop app** OAuth client (no redirect registration needed), and the web UI walks through pasting the resulting URL back. `external_url` remains for installations with a public https hostname in front of port 8098, where the redirect completes automatically.
- Web UI and docs say where the web UI is (the add-on's **Open Web UI** button / sidebar entry).

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
