<p align="right">
  <a href="README.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Voice Agent Terminal

A handheld voice-chat terminal with editable message drafts, a Go gateway,
OpenAI transcription, and Hermes Chat Completions.

## Project status

The Go gateway implements mDNS, persistent sessions and editable drafts,
OpenAI realtime transcription, Hermes Chat Completions, media filtering and
message recovery. Offline adapter tests and a real-binary smoke test exercise
the server workflow. Live provider validation needs local credentials.
Device pages and physical interactions will be designed after the server.
The existing [voice-memo](https://github.com/tf4fun/voice-memo) project provides
hardware and reusable server foundations.

## Run the server

Use Go 1.27.3 or later.

```sh
cd server
cp .env.example .env.local
# Fill in OpenAI and Hermes settings in .env.local.
go run ./cmd/voice-agent-server
```

The binary reads the environment file literally. Process variables take
precedence. `/api/v1/health` reports local service health and `/api/v1/server`
reports capabilities. Missing upstream keys leave the corresponding capability
disabled while the local service remains inspectable.

See [server configuration and API](docs/server.md) for request examples.

## Validate and build

```sh
cd server
go vet ./...
go test -race ./...
cd ..
./tools/smoke-server.sh
./tools/build-server.sh amd64
./tools/build-server.sh arm64
```

GitHub Actions runs the server checks and builds static Linux AMD64 and ARM64
binaries with SHA-256 checksums. Artifacts are attached to each successful run.

## Interaction

1. Record a thought and view streaming transcription in a draft segment.
2. Review the draft, record additional segments, and delete unwanted segments.
3. Explicitly send the visible draft as one text message in the selected session.
4. Read the streamed reply and continue the conversation.

Each recording creates an independently editable draft segment. Sending commits
the reviewed draft revision as an immutable message snapshot. Transcription
completion updates the editable draft; the user's send action triggers Hermes.

## Architecture

```mermaid
flowchart LR
    Device[Handheld terminal] <-->|Local device API| Gateway[Go gateway]
    Gateway <-->|Realtime transcription| OpenAI[OpenAI]
    Gateway <-->|Chat Completions and sessions| Hermes[Hermes gateway]
```

- The Go gateway advertises itself through mDNS and provides the device API.
- OpenAI supplies streaming transcription. `gpt-live-transcribe` is the initial
  candidate from the official guidance checked on 2026-11-13.
- Hermes manages conversation history and serves Chat Completions through its
  existing API.
- Go manages the device's session list, drafts, message identities, filtered
  reply cache, and reconnect recovery.
- The device uses bounded audio and text buffers. Media filtering happens in Go.

AI Passport, with ESP32-C3 and no PSRAM, is the initial validation device.
Future hardware may use ESP32-S3 with PSRAM. Audio transport, encoding, memory
budgets, and final button mappings remain design and measurement work.

## Documentation

- [Architecture and data ownership](docs/architecture.md)
- [Milestones and acceptance](docs/roadmap.md)
- [Server configuration and API](docs/server.md)
- [Agent contribution instructions](AGENTS.md)

## References

- [OpenAI realtime transcription](https://developers.openai.com/api/docs/guides/realtime-transcription)
- [Hermes API server](https://hermes-agent.nousresearch.com/docs/user-guide/features/api-server/)
- [AI Passport hardware and firmware reference](https://github.com/FoloToy/ai-passport)

Provider availability, session continuation, retry behavior, microphone latency,
and device memory usage require integration tests against the selected versions.

## License and reuse

This project follows the MIT license retained in [LICENSE](LICENSE).
[NOTICE](NOTICE) records the server foundations adapted from voice-memo and
its FoloToy/ai-passport lineage.
