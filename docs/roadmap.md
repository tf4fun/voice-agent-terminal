<p align="right">
  <a href="roadmap.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Milestones

All implementation milestones below are planned. Repository initialization
establishes the requirements and acceptance criteria.

| Milestone | Deliverable | Acceptance |
| --- | --- | --- |
| M1 Gateway and protocol | Go service, configuration, mDNS, capabilities, bounded device API | Discovery reaches the advertised service; startup and shutdown are clean; schemas and limits are documented |
| M2 Drafts and sessions | Persistent draft segments, revision checks, paginated session index, Hermes session mapping | Append/delete/reconnect work; late ASR updates respect segment lifetime; sending commits the displayed revision |
| M3 OpenAI transcription | Incremental audio intake, upstream realtime session, transcript events | Representative Chinese audio produces partial and final text; segment identities remain correct; backpressure stays bounded |
| M4 Hermes chat | Explicit draft submission, session continuation, filtered SSE, message recovery | One reviewed draft produces one logical message; exact retries, long text, fragmented media, errors and reconnects are exercised |
| M5 AI Passport client | Discovery, session selection, recording, draft review/delete/append/send and replies | Device journeys work; measured heap, largest block and stack margins support capture plus networking |
| M6 Integrated validation | Repeated full interactions and recovery tests | Reboot during draft/send, upstream loss, slow links, duplicate requests and long replies have observable outcomes |

## Decisions to resolve through implementation

- Device audio encoding, transport, sample rate and buffer limits.
- Draft text editing granularity and physical button mappings.
- Device API routes, event sizes, text windows and session page size.
- Audio and reply-cache retention.
- Presentation of pending approvals and unavailable media.
- Provider permissions, latency and accuracy using the supplied credentials.

## Validation records

Record gateway builds/tests, provider integration tests and device tests
separately. Device measurements identify the board, firmware build and workload.
Successful host checks establish software behavior under those checks; on-device
interaction, audio, memory and timing require their own evidence.
