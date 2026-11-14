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
| Device | Selected session, reviewed draft revision, one complete user input and one complete Hermes reply in RAM, local reading position, bounded audio transport buffers and interaction state |
| Go | Device-to-Hermes session mapping, paginated session index, persistent drafts and segment order, complete message snapshots and retry identities, bounded SSE delivery and filtered reply storage |
| Hermes | Conversation history, model context, session runtime and configured agent capabilities |
| OpenAI | Transcription of the audio submitted by Go |

Go's session index serves the device's view of Hermes sessions. Creation and
deletion have persisted pending states for interrupted-operation recovery.
Go forwards created and renamed titles to Hermes while retaining the device index.
The device holds the selected complete input and reply in RAM. Historical
navigation selects another complete exchange through message IDs and neighboring
exchange IDs. Complete text responses preserve stored UTF-8 and revision identity.

## Message reading and waiting

A message remains one object throughout loading, streaming and reading. The
selected message is presented in one locally scrollable card. Up/down input
moves the reading position inside its cached complete body. History navigation
loads another complete user/assistant exchange when requested. RAM allocation
follows the selected text's actual size, and leaving the conversation releases
its cache. Session-list pagination remains independent of message reading.

Hermes replies arrive at the device cache through SSE. The card displays its
status graphic and animation throughout generation. Completion reveals the
whole reply at its beginning. Up/down input controls the local reading position.
The compact header combines the page title, time and battery. The role and
status share the card's first row. The user can view their own input while
waiting. An interrupted or failed reply
reveals any retained text together with its outcome status.

Firmware `9b06632b` passed authorized flashing, startup/discovery and one
recording/reply workload. The user confirmed improved SSE appending and correct
Chinese quotation marks. Firmware `077092ec` subsequently passed user acceptance
of the compact header/status layout, card spacing, completion waiting, top-of-reply
opening and manual scrolling. Long-message stress and reconnect recovery require
separate device checks. Measurements and results describe their recorded builds.

## Draft composition

Each recording creates a segment ID with an ordered position and a generation.
Streaming recognition updates that segment while the draft remains editable.
The gateway API supports appending recordings and deleting selected segments.
The current device interaction reviews a pending message in the conversation,
sends it with Short OK, and re-records it with Long OK. Replacement capture keeps
earlier text until a nonempty final transcript atomically retires it. Complete
draft reads use the same joining rules and revision as send snapshots.

Edits and transcription updates advance the draft revision. A deleted segment
is retired, and late updates are checked against its ID, generation and lifetime.
The device sends its reviewed revision with a send request. Go checks that
revision and atomically commits the matching text as a message snapshot. A stale
revision returns the current draft for review before a new send attempt.

This revision check preserves the user's visible text across late recognition
updates. Explicit send is the sole trigger for submitting draft text to Hermes.
A message's text is fixed after submission. Draft and send failure recovery
retain the user's input and its operation identity.

Capture results are staged before the device automatically confirms replacement.
Cancellation, restart recovery and expiry retire uncommitted staged text while
preserving the prior draft. Commit and abort reconcile through a transaction and
persisted commit revision. Text reads isolate late responses by request generation,
and discovery rotates endpoints after a rejected candidate.

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

The device streams mono PCM16 at 16 kHz over chunked HTTP and requires a
connected gateway. An 8 KiB audio buffer bounds capture buffering. Bounded live
transcription previews serve recording, and committed input is read as complete
text for review. Hardware acceptance of the new complete-message revision
remains NOT RUN.
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

For device recovery, establish the session SSE subscription before reading the
complete message snapshot. The initial `resync` event triggers metadata and full
body reads. Returned UTF-8 byte lengths establish the delta baseline. Matching
`chat.delta` offsets append to the same reply, already represented offsets are
deduplicated, and gaps or `chat.reset` trigger a fresh complete snapshot.
Revision and state headers belong to the same stored body read. Disconnect
recovery reloads that complete message and reconciles subsequent SSE events.

## Bounded content delivery

Go incrementally parses upstream SSE and JSON with explicit limits on events,
fields, pending prefixes and reply-cache growth. Media markers and base64 blocks
may span fragments. Filtering must preserve following text and valid UTF-8.
Represent omitted media with a small resource description. Deliver complete
message bodies and bounded incremental events. Transport event limits and RAM
allocation checks preserve the logical identity and complete stored message.

Approval events, reasoning and progress have explicit presentation policies.
The initial device feature set centers on text chat. Pending approvals retain
their waiting state and produce a concise attention status; detailed approval
interaction remains an open product decision.

## Discovery and device API

The mDNS record names the Go service and advertises a protocol version. The
gateway's capability response describes available chat, draft and transcription
features. The implemented service type, routes, payload bounds and recovery
semantics are recorded in the [server contract](server.md).

The server accepts HTTP PCM16 audio uploads and sends bounded SSE events. Audio
upload and reply delivery have independent flow control. The device reads whole
message snapshots and appends reply events into its selected RAM cache. Metadata
polling supports discovery and recording state. Device memory and latency for
this revision need hardware measurements. OpenAI's realtime transport runs in Go.

## References

- [OpenAI realtime transcription](https://developers.openai.com/api/docs/guides/realtime-transcription)
- [Hermes API server](https://hermes-agent.nousresearch.com/docs/user-guide/features/api-server/)
- [Hermes session storage](https://hermes-agent.nousresearch.com/docs/developer-guide/session-storage/)
