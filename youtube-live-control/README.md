# YouTube Live Control add-on

Home Assistant add-on that runs a channel's scheduled YouTube live broadcasts from one MQTT panel. A volunteer on an iPad picks or creates a broadcast, edits the title, goes live, and ends it after the service.

- One **Broadcast** select (upcoming broadcasts + *New stream…*) with a shared detail panel: Title, Scheduled start, Thumbnail, health/status/viewer sensors, and Save / Create / Go Live / End Stream buttons
- OAuth 2.0 (`youtube.force-ssl`) against your own Google client; consent once in the ingress web UI, refresh token kept in `/data`
- Every write is verified and read back before Home Assistant updates; Go Live and End Stream are gated on the encoder's real stream status
- Quota-aware tiered polling (idle 10 min / live 60 s / fast 3 s), with a **Fast refresh** switch auto-armed by any panel interaction and expired by the add-on itself
- Poll cadences are runtime settings entities persisted in `/data`; add-on options carry only OAuth and infrastructure
- Single static Go binary; `aarch64` and `amd64`

See [DOCS.md](DOCS.md) for setup and behaviour and the [repository README](../README.md) for architecture and development.
