<p align="right">
  <a href="architecture.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Architecture

## Agreed boundaries

The handheld connects to the Go gateway discovered through mDNS. Go integrates
with the existing Hermes API and OpenAI transcription and speech synthesis.
Credentials and upstream TLS connections live on the gateway.

The initial chat integration uses Hermes `POST /v1/chat/completions` with
`X-Hermes-Session-Id` and streaming replies. Go coordinates session creation,
listing and deletion through Hermes' existing session API. Verify these behaviors
against the selected Hermes version before depending on them.

## Ownership

| Owner | Data and responsibilities |
| --- | --- |
| Device | Selected session, the unsent voice draft (RAM only, up to 140 characters), one complete user input and one complete Hermes reply in RAM, local reading position, bounded audio capture and playback buffers and interaction state |
| Go | Device-to-Hermes session mapping, paginated session index, side-channel transcription that stores nothing, complete message snapshots and retry identities, bounded SSE delivery, filtered reply storage and the cached spoken-reply streams |
| Hermes | Conversation history, model context, session runtime and configured agent capabilities |
| OpenAI | Transcription of the audio submitted by Go and synthesis of the completed reply text Go submits for speech |

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

While Hermes writes a reply the device polls only the turn's state, then
fetches the complete reply into its cache (the web page streams it through
SSE). The card displays its status graphic and animation throughout generation. Completion reveals the
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

## Drafts and sessions

Since protocol 2 (2026-11-16) a draft belongs to the client composing it and
never reaches the gateway before it is sent. Voice input is equivalent to
typing: transcription is a side channel that returns text and persists nothing.

- The device keeps one pending message in RAM, bounded to 140 characters and
  shown with its character count. A successful re-recording replaces it, a
  failed one keeps it, long Down clears it and leaving the session discards it.
  It does not delay sleep; deep sleep or a reboot loses it.
- The web page keeps unsent composer text per session in page memory.
- Sending carries the complete text with a client message ID. A new chat has a
  client-generated session ID only, and its first send creates the session in
  the same transaction as the message, titled from the message's first line.
  No session exists without a message, so an abandoned recording or a dropped
  connection leaves nothing behind for the gateway or Hermes to clean up.
- A repeated send with the same IDs and text returns the stored message, so a
  lost response is retried without duplicating it. A message's text is fixed
  once stored; explicit send is the sole trigger for submitting it to Hermes.
- The two clients' drafts do not synchronize; sent messages and replies do.

## Audio and transcription

The target is incremental audio upload and visible transcription while recording.
Go owns the OpenAI realtime connection, converts the selected device audio format
to an accepted upstream format, and associates events with the recording's
transcription ID. Ending a recording commits that audio turn; the final text is
the upload's response and becomes the client's draft. Recognition finalization
and message sending are separate user-visible transitions.

The initial model candidate is `gpt-live-transcribe`. The official realtime guide
checked on 2026-11-13 demonstrates mono PCM16 at 24 kHz and explicit audio-turn
commit. Confirm account access, accepted formats and latency using real audio.
Match upstream events by item identity.

The device streams mono PCM16 at 16 kHz over chunked HTTP and requires a
connected gateway. A 16 KiB PCM buffer, shared with spoken-reply playback,
bounds capture buffering. Bounded live
transcription previews serve recording, and the final text is held in RAM for
review. Hardware acceptance of the new complete-message revision
remains NOT RUN.
Benchmark capture and networking together on AI Passport, including total free
heap, largest free block, stack high-water marks and bounded-backpressure behavior.
The current voice-memo recording/upload lifecycle is an implementation reference.

## Spoken replies

When the gateway reports `speech`, the device reads each new newest reply of the
open session aloud. Go waits until Hermes has finished the reply, removes
Markdown marks, code blocks and link targets, and synthesizes the whole reply in
one OpenAI Realtime request. Go frames the 16 kHz PCM16 result and caches the
stream, so listeners share one synthesis and a dropped connection resumes at its
exact byte offset. The device plays it through the PCM buffer it shares with
recording; recording, a long Down and leaving the session stop playback. Framing,
limits and provider settings are in the [server contract](server.md#spoken-replies)
and playback buffering is in the
[firmware guide](firmware.md#voice-first-sessions-and-spoken-replies). Device
acceptance of spoken replies on the newest build needs its own evidence.

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

For SSE clients (the web page) recovery, establish the session SSE subscription before reading the
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

Text has one hard bound per stage. A default system prompt
(`HERMES_SYSTEM_PROMPT`) asks Hermes for replies within 300 characters unless a
long one is requested, so the bounds below are a backstop:

| Stage | Bound | Beyond it |
| --- | --- | --- |
| Device voice draft | 140 characters | The recording is refused and the earlier draft kept |
| Message input (any client) | 64 KiB | `413 input_too_large` |
| One transcript | 16 KiB | `413` |
| Transcripts held by the gateway | 16, one minute after each ends | The oldest finished one is dropped |
| Stored reply | 2 MiB | The message ends `incomplete`; the web page shows what was kept |
| Spoken text | 8000 bytes (about 2600 Chinese characters) | Not read aloud |
| Spoken audio | 16 MiB (about 8.7 min), 10 min per reply | Synthesis stops |
| Device input and reply cache | 8000 bytes each | A notice points to the web page |
| Voice page ticker | 256 lines (about 7.5 KiB) | Not reachable on that page |

Approval events, reasoning and progress have explicit presentation policies.
The initial device feature set centers on text chat. Pending approvals retain
their waiting state and produce a concise attention status; detailed approval
interaction remains an open product decision.

## Discovery and device API

The mDNS record names the Go service and advertises a protocol version. The
gateway's capability response describes available chat, transcription and
speech features. The implemented service type, routes, payload bounds and recovery
semantics are recorded in the [server contract](server.md).

The server accepts HTTP PCM16 audio uploads and sends bounded SSE events to the
web page. Audio upload and reply delivery have independent flow control. The
device polls turn metadata while a reply is written and then reads the whole
message snapshot into its selected RAM cache. While recording it polls the live
transcript of its transcription ID. Device memory and latency for
this revision need hardware measurements. OpenAI's realtime transport runs in Go.

## References

- [OpenAI realtime transcription](https://developers.openai.com/api/docs/guides/realtime-transcription)
- [Hermes API server](https://hermes-agent.nousresearch.com/docs/user-guide/features/api-server/)
- [Hermes session storage](https://hermes-agent.nousresearch.com/docs/developer-guide/session-storage/)
