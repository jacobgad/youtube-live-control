# YouTube Live Control add-on

Home Assistant add-on that runs a channel's scheduled YouTube live streams. Producers schedule from presets in the web UI; volunteers go live and end from one MQTT panel.

- **Scheduling device**: Preset · Date · Time · Schedule — creates the preset's broadcast at the chosen half-hour slot
- **Selected-broadcast device**: Broadcast select (soonest first), Title and Privacy applied immediately, a **Stage** enum sensor, Scheduled start timestamp, Live / Encoder connected binary sensors, Go Live / End Stream
- **Web UI**: presets (title pattern, description, privacy, stream key, thumbnail, usual day/time), edit broadcasts, off-pattern scheduling
- OAuth 2.0 (`youtube.force-ssl`) against your own Google client; consent once in the ingress web UI, refresh token kept in `/data`
- Every write is verified and read back before Home Assistant updates; Go Live and End Stream are gated on the encoder's real stream status
- Quota-aware tiered polling (idle 10 min / live 60 s / fast 3 s), with a **Fast refresh** switch auto-armed by any panel interaction and expired by the add-on itself
- Configuration (OAuth client, poll cadences) lives in the add-on options; the MQTT device carries only what operates YouTube
- Single static Go binary; `aarch64` and `amd64`

See [DOCS.md](DOCS.md) for setup and behaviour and the [repository README](../README.md) for architecture and development.
