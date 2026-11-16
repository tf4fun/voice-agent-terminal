<p align="right">
  <a href="AGENTS.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Agent instructions

Read [README](README.md), [architecture](docs/architecture.md), and the relevant
[milestone](docs/roadmap.md) before changing code. Start with
`git status --short --branch` and preserve existing work.

- Keep the Go gateway, device client and upstream adapters in clear components.
  The gateway owns discovery, transcription (a side channel that stores
  nothing), the session index, content filtering and message recovery.
- Drafts stay on the client that composes them. An explicit send carries the
  complete text, and a new session is created only by its first send. Preserve
  message IDs and immutable message snapshots across failures and retries.
- Bound audio, JSON, SSE and text buffers. Validate protocol/state behavior with
  host tests and measure actual device memory before claiming hardware readiness.
- Follow the hardware reference for pins and peripherals. Keep button callbacks
  non-blocking and hold the board's LVGL lock for cross-task UI access.
- Verify OpenAI and Hermes APIs against official documentation and the deployed
  versions. Keep credentials in local service configuration and sanitize logs.
- Keep recordings, transcripts, databases, keys and generated build artifacts
  outside Git. Document example configuration with placeholders.
- Keep each English Markdown document paired with Simplified Chinese at the
  corresponding `.zh_CN.md` path and maintain reciprocal language links.
- Implement the requested milestone with focused checks. Report builds, host
  tests, upstream integration and device tests separately.
- Obtain device-flash approval for the specific build. Create commits and push
  when authorized by the user or the active workflow.

Keep implementation status and measured results aligned with the roadmap.
Live provider and device acceptance need their own evidence.
