<p align="right">
  <a href="architecture.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Architecture

## Agreed boundaries

The handheld connects to the Go gateway discovered through mDNS. Go integrates
with the existing Hermes API and OpenAI transcription. Credentials and upstream
TLS connections live on the gateway.

The initial chat integration uses Hermes `POST /v1/chat/completions` with
`X-Hermes-Session-Id` and streaming replies. Go coordinates session creation,
listing and deletion through Hermes' existing session API. Verify these behaviors
against the selected Hermes version before depending on them.

## Ownership

| Owner | Data and responsibilities |
| --- | --- |
| Device | Selected session, visible draft revision, current text window, bounded audio transport buffers, interaction state |
| Go | Device-to-Hermes session mapping, paginated session index, persistent drafts and segment order, message snapshots and retry identities, bounded device event delivery, filtered reply cache |
| Hermes | Conversation history, model context, session runtime and configured agent capabilities |
| OpenAI | Transcription of the audio submitted by Go |

Go's session index serves the device's view of Hermes sessions. Creation and
deletion have persisted pending states for interrupted-operation recovery.
Device titles are managed locally in Go. History reads through Go use bounded
text windows.

## Draft composition

Each recording creates a segment ID with an ordered position and a generation.
Streaming recognition updates that segment while the draft remains editable.
The user can append recordings and delete selected segments.

Edits and transcription updates advance the draft revision. A deleted segment
is retired, and late updates are checked against its ID, generation and lifetime.
The device sends its reviewed revision with a send request. Go checks that
revision and atomically commits the matching text as a message snapshot. A stale
revision returns the current draft for review before a new send attempt.

This revision check preserves the user's visible text across late recognition
updates. Explicit send is the sole trigger for submitting draft text to Hermes.
A message's text is fixed after submission. Draft and send failure recovery
retain the user's input and its operation identity.

## Audio and transcription

The target is incremental audio upload and visible transcription while recording.
Go owns the OpenAI realtime connection, converts the selected device audio format
to an accepted upstream format, and associates events with the recording segment.
Ending a recording commits that audio turn and settles its transcription in the
draft. Recognition finalization and message sending are separate user-visible
transitions.

The initial model candidate is `gpt-live-transcribe`. The official realtime guide
checked on 2026-11-13 demonstrates mono PCM16 at 24 kHz and explicit audio-turn
commit. Confirm account access, accepted formats and latency using real audio.
Match upstream events by item identity and segment generation.

The device audio codec, sample rate, transport and offline behavior remain open.
Benchmark capture and networking together on AI Passport, including total free
heap, largest free block, stack high-water marks and bounded-backpressure behavior.
The current voice-memo recording/upload lifecycle is an implementation reference.

## Chat and recovery

The gateway submits a committed text snapshot to the mapped Hermes session.
Persist one message identity before submission and reuse its upstream idempotency
key for an exact retry. Serialize sending within a session so ordering is clear.
The selected Hermes mode's deduplication and session-continuation behavior need
direct tests.

Go tracks sending, waiting, receiving, completed, incomplete, failed and
unconfirmed outcomes. Preserve the upstream finish reason and expose a clear
device state after a broken connection. Reconnection reconciles the same message
with cached output or upstream state. Active execution, retry evidence and reply
delivery have separate lifetimes.

## Bounded content delivery

Go incrementally parses upstream SSE and JSON with explicit limits on events,
fields, pending prefixes and reply-cache growth. Media markers and base64 blocks
may span fragments. Filtering must preserve following text and valid UTF-8.
Represent omitted media with a small resource description. Deliver long text
through bounded events and versioned read windows.

Approval events, reasoning and progress have explicit presentation policies.
The initial device feature set centers on text chat. Pending approvals retain
their waiting state and produce a concise attention status; detailed approval
interaction remains an open product decision.

## Discovery and device API

The mDNS record names the Go service and advertises a protocol version. The
gateway's capability response describes available chat, draft and transcription
features. The implemented service type, routes, payload bounds and recovery
semantics are recorded in the [server contract](server.md).

The server currently accepts HTTP PCM16 audio uploads and sends bounded SSE
events. Audio upload and event delivery have independent flow control. Device
transport selection still needs measured memory and latency. OpenAI's realtime
transport runs in Go.

## References

- [OpenAI realtime transcription](https://developers.openai.com/api/docs/guides/realtime-transcription)
- [Hermes API server](https://hermes-agent.nousresearch.com/docs/user-guide/features/api-server/)
- [Hermes session storage](https://hermes-agent.nousresearch.com/docs/developer-guide/session-storage/)
