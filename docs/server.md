<p align="right">
  <a href="server.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Go server

## Configuration

Copy [server/.env.example](../server/.env.example) to `server/.env.local` and
start the binary from `server`, or pass `-env /absolute/path/to/file`.
The file contains literal `NAME=value` lines and optional matching outer quotes.
Existing process variables take precedence.

| Variable | Meaning |
| --- | --- |
| `VOICE_ADDR` | Listen address, default `:8080`; CLI `-addr` overrides it |
| `VOICE_DATA_DIR` | Gateway SQLite directory, default `./data` |
| `VOICE_MDNS` | Advertise on an active LAN, default `true` |
| `VOICE_MDNS_NAME`, `VOICE_MDNS_INTERFACE` | Display name and optional LAN interface |
| `VOICE_API_KEY` | Optional bearer credential for device requests; health remains readable |
| `VOICE_CHAT_WORKERS` | Concurrent session workers, default 2, range 1–8 |
| `HERMES_BASE_URL` | Hermes API root, default `http://127.0.0.1:8642`; a profile prefix can be included |
| `HERMES_API_KEY` | Hermes API server credential |
| `HERMES_MODEL` | Request model/route alias, default `hermes-agent` |
| `HERMES_SYSTEM_PROMPT` | System message that leads every chat request; Hermes layers it on its own prompt. Each message stores the prompt it was sent with, so a change applies to new messages and a retry repeats the original. The default asks for replies within 300 characters unless a long one is requested |
| `VOICE_ASR_PROVIDER` | `openai` by default; `fake` for offline transcription tests |
| `OPENAI_API_KEY` | OpenAI credential, held by Go |
| `OPENAI_TRANSCRIBE_MODEL` | Default `gpt-live-transcribe` |
| `OPENAI_REALTIME_URL` | Default `wss://api.openai.com/v1/realtime?intent=transcription` |
| `VOICE_TTS_PROVIDER` | Spoken replies: `openai` by default (uses `OPENAI_API_KEY`), `fake` or `off` |
| `OPENAI_TTS_MODEL`, `OPENAI_TTS_VOICE`, `OPENAI_TTS_URL` | Defaults `gpt-realtime-2.1-mini`, `marin` and `wss://api.openai.com/v1/realtime`; see [Spoken replies](#spoken-replies) |

Local data and environment files are ignored by Git. Bind network exposure and
device credentials to the intended LAN deployment. A configured Hermes API
server provides the matching `HERMES_API_KEY` value and API-created sessions.

## Web chat

Open the server root, such as `http://localhost:8080/`, for a chat client that
shares sessions and replies with the device. Its layout follows common
chat web apps: a sidebar of sessions grouped by recent activity with title
search, rename and delete, and a conversation column with a composer. Colors
reuse the device's light and dark palette roles; the theme follows the system
or is chosen in the sidebar. Narrow screens use a slide-in sidebar.

- Replies stream live through `GET /api/v1/events`, read with `fetch` so the
  bearer key can be sent. Deltas apply at their UTF-8 byte offsets; a gap or
  `chat.reset` refetches the complete reply, and a reconnect reconciles the
  newest page of messages. Replies render a safe Markdown subset (headings,
  lists, quotes, tables, code blocks with copy, links) built as DOM nodes,
  never as HTML strings.
- The composer text stays in the page (per session, until sent or reloaded);
  the device's voice draft stays on the device. Sending posts the complete
  text. A new conversation has no session until its first send, which creates
  it and is titled from its first line; a lost response is retried with the
  same IDs.
- A message sent from the device appears as soon as its first event arrives,
  and the view follows it to the bottom while its reply streams.
- Failed or unconfirmed replies offer retry on the latest exchange. Messages and
  code blocks can be copied, including on plain-HTTP LAN pages.
- `#/c/<session-id>` links open a session directly. Ctrl/Cmd+K starts a new
  conversation; Enter sends and Shift+Enter inserts a line break.

If `VOICE_API_KEY` is configured, enter that gateway key on the page. It stays in
page memory unless "remember in this browser" is checked, which stores it in
`localStorage` until sign-out. The public HTML/CSS/JavaScript/icon shell
contains no session data or credentials; API requests use bearer
authentication and same-origin mutation protection, and the page's content
security policy allows only same-origin resources. The assets are compiled into
the Go binary with the standard library's `embed` support.

Deletion uses the existing session API and synchronizes the corresponding
Hermes session. A pending deletion remains visible until synchronization
completes. Active or unconfirmed messages produce the existing busy response.
The page lists sessions managed by this gateway.

## Discovery and limits

Go publishes `_voiceagent._tcp.local` with `ver=1`, `path=/api/v1`, capabilities
and an IPv4 fallback. Loopback listeners skip mDNS. An advertisement failure is
logged while HTTP remains available. The device validates `/api/v1/ping` and
reads `/api/v1/server` after discovery.

- Session, transcription and message IDs use lowercase UUID syntax.
- Metadata pages default to 4 entries and accept `limit=1..20` and an offset.
  `has_more` reports whether another page exists without an empty-page probe.
- A message input is bounded to 64 KiB and a transcript to 16 KiB.
- Live-transcription and optional window reads use UTF-8 byte offsets and windows
  up to 2048 bytes. Complete-message content responses carry the full stored body.
- Each SSE frame is at most 1024 bytes. Delta text uses at most 64 bytes.
- Each filtered reply holds up to 2 MiB. Sanitized upstream SSE events have a
  256 KiB bound. Oversized ordinary events produce an explicit unconfirmed outcome.
- Up to four audio uploads and 64 SSE subscribers run concurrently. Audio
  duration is bounded to five minutes. The complete upload/recognition
  request has a six-minute budget for setup, drain and finalization. Chat timeout
  is ten minutes.

## Sessions

A session is created by its first message (see [Explicit send](#explicit-send-and-replies)):
there is no separate create request, so no session exists without a message.
Creation is persisted before contacting Hermes, titled from the first non-blank
line of the message (at most 24 characters, then `…`). A failed Hermes create
leaves the session `creating` with its message queued; background recovery
retries the same upstream ID and the message is sent once the session is
ready. Hermes 409 responses are reconciled against the existing API session.
If Hermes rejects a title with `400 invalid_title`, creation retries once without
the title using the same session ID, and the local index keeps its title.
Renaming verifies upstream ownership and updates Hermes before the local title.
An upstream failure retains the previous local title for an explicit retry.
The index covers sessions created through Go.

`GET /api/v1/sessions` lists the local session index. `GET` and `PATCH` on
`/api/v1/sessions/{id}` read metadata and update the Hermes and local title.
`DELETE` marks a session as deleting, coordinates upstream deletion and retains
a retired local ID. Active or unconfirmed messages return a busy response,
except in a session still `creating`: its queued first message has never been
sent, so the session can always be deleted and the message goes with it. A
create that succeeds afterwards is deleted upstream as well.
An upstream session's API source is verified before deletion.

## Transcription

Transcription is a side channel since protocol 2 (2026-11-16): it turns one
recording into text for the client's own draft and stores nothing in SQLite.
Drafts never reach the gateway before they are sent.

The gateway keeps one configured OpenAI realtime transcription session warm.
OpenAI's realtime transcription guide sends many audio turns over one session:
commit at the end of each turn and match transcription events by `item_id`,
because completion order across turns is not guaranteed. Each recording sends
`input_audio_buffer.clear`, appends its audio, commits once and accepts only the
first item after its clear. A cleanly finished session returns to the pool; a
failed or cancelled turn discards its session, because buffered audio or an
in-flight item may remain. A dedicated reader per connection answers keepalive
pings sent every 20 s, detects drops at once and triggers a reconnect with
exponential backoff up to 60 s. Sessions are replaced after 50 minutes, inside
the documented realtime session limit. A second concurrent recording opens its
own session.

`POST /api/v1/transcriptions` with `{"id":"<uuid>"}` reserves the
warm session, or opens one, before the `201` response, waiting at most 3 s,
below the device's 4 s request timeout. The device keeps its preparing
animation until that response, so capture starts against a ready transcriber.
The reserved session belongs to that transcription ID, is handed to exactly one
upload, and returns to the pool after 30 s unused or when the gateway stops. Logs record the
preparation time and any transcription failure code without credentials.

Audio uploads are also drained from the first byte, covering a handshake slower
than 3 s or an upload without a prepared session. A cold handshake through a proxy takes about 2.5 s; before this
change the gateway did not read the body until `session.updated`, so TCP
backpressure overflowed the device's 8 KiB PCM queue at 2.8-2.9 s. The prefetch
holds up to 16 MiB per capture, more than a full 300 s 16 kHz recording, and
allocates chunks only as audio arrives. The request's byte limit stays in force.

`POST /api/v1/transcriptions/{id}/audio?sample_rate=16000` streams raw mono
little-endian PCM16 with `Content-Type: audio/pcm`. Supported input rates are
16000 and 24000. The request can be chunked; EOF commits the audio turn. Go
incrementally resamples 16 kHz input to 24 kHz and connects to OpenAI. A
successful response is `200` with the final transcript, trimmed, as the whole
`text/plain; charset=utf-8` body. A blank transcript returns `422 no_speech`,
a reused ID `409`, a full set of four concurrent uploads `409 busy`, an
oversized upload `413` and an upstream failure `502 transcription_failed`.
Closing the connection cancels the transcription. Audio bytes are transient.

`GET /api/v1/transcriptions/{id}/text` reads the live text of a running
transcription through the bounded UTF-8 window API (`offset`, `limit`,
`before=<byte boundary>` or `tail=1`; the revision counts updates). The text is
kept in memory while the upload runs and for one minute after it ends, at most
16 transcriptions at once (the oldest finished one gives way); an unknown or
released ID returns 404, including before the upload has started.

`/api/v1/server` reports `protocol: 2` with `bounded_paging`, `text_before`,
`text_tail`, `conversation_windows`, `whole_messages`, `reply_sse`,
`max_input_bytes`, `max_transcript_bytes`, `device_logs` and `speech`; the
device requires protocol 2. SQLite schema v5 holds sessions, messages (with
the system prompt each was sent with) and device reports only. An earlier database is refused at startup rather than
migrated: remove `gateway.db` to start empty.

## Complete messages

The device and management page select one complete user/assistant exchange with
`GET /api/v1/sessions/{id}/turn`. The response contains `message` metadata and
`before`/`after` message IDs. A session with no sent messages returns
`{"message":null,"before":"","after":""}`. `index` (1-based) and `total` give
the exchange's position among the session's exchanges. Select one historical exchange with
exactly one `message=<uuid>`, `before=<uuid>` or `after=<uuid>` query. Neighbor
selection is scoped to the current session and returns 404 at either boundary.
Malformed or repeated selectors return 400.

Read complete UTF-8 bodies from
`GET /api/v1/sessions/{id}/messages/{message}/content?part=input|reply`.
The plain-text response includes exact byte `Content-Length`, `X-Revision`,
`X-Message-State`, and `Cache-Control: no-store`. Body text retains original
newlines and formatting. Whole-message responses carry no display-page offsets
or page counts.

An optional `revision=<n>` requires the exact snapshot and returns 409 if it has
advanced. For streaming recovery, subscribe to SSE first and then read the
current complete reply. Its returned byte length establishes the delta offset.
The header revision and state describe that same stored snapshot. The device
keeps the selected input and reply in RAM and loads a different exchange when
history navigation requests it. Capabilities advertise `whole_messages` and
`reply_sse`.

## Conversation windows

`GET /api/v1/sessions/{id}/conversation` returns up to four chronological blocks,
ending at the newest reply by default. Each block identifies the message,
`part` (`input` or `reply`), state, revision, UTF-8 byte offsets and at most
256 bytes of text. An empty in-progress reply retains its assistant block.
The response includes `before`, `after` and `start` cursors. Pass exactly one
as `before=<cursor>`, `after=<cursor>` or `from=<cursor>` to move or refresh.
Adjoining pages share one block so the device can retain its visible anchor.
Cursor reads are scoped to the selected session. A stale text boundary returns
409 and the client reloads a fresh window while keeping its existing view.
The capability flag is `conversation_windows`.

## Explicit send and replies

`POST /api/v1/sessions/{id}/messages` accepts
`{"id":"<message-uuid>","text":"<complete input>","new_session":<bool>}`.
The text is the client's reviewed draft: nonblank (else `400 empty_input`) and
at most 64 KiB (else `413 input_too_large`). One transaction persists it as an
immutable queued message. With `new_session`, an unknown session ID is created
in the same transaction and titled from the text; without it an unknown
session returns 404, as does a deleting one. The response is 202 with the
message metadata plus a `session` object (ID, title, state), from which a client
learns a new session's title. Repeating the same message ID with the same
session and text returns the same message, so a lost response is retried
safely; the same ID with other text returns 409. A session with an active or
unconfirmed message returns busy.

Workers send one new user message to Hermes Chat Completions with its session ID
and a stable idempotency key. The stored model, system prompt, input and upstream
identity hash remain associated with the message, so a retry sends the same request. Concurrent sends within one session are
serialized by the store's active-message constraint.

`GET /api/v1/sessions/{id}/messages` lists message metadata.
`GET /messages/{message}` reads state. `GET /messages/{message}/text` reads a
reply window, with `part=input` for the submitted snapshot.

| State | Meaning |
| --- | --- |
| `queued`, `sending`, `waiting` | Persisted, claimed, or awaiting upstream output |
| `receiving` | Reply text is being persisted |
| `attention` | The upstream reported a pending approval |
| `completed` | The stream ended with `finish_reason: stop` and `[DONE]` |
| `incomplete`, `cancelled`, `failed` | Explicit upstream outcome or reply-cache limit |
| `unconfirmed` | Connection loss, restart, invalid stream or changed upstream identity |

`POST /messages/{message}/retry` retries the same logical message after a failed
or unconfirmed outcome, provided later messages have not advanced the session.
Recovery uses the same Chat Completions request identity with `stream: false`
to obtain a complete reply. It atomically replaces the earlier partial reply.
A changed endpoint or credential blocks automatic submission under the new identity
and marks the message `unconfirmed` with `hermes_identity_changed`. An explicit
retry confirms resubmission and moves the message to the current identity scope.
This recovery targets Hermes' canonical durable-admission mode; verify the
deployed mode's idempotency behavior before live use. Legacy streaming retries
have different guarantees.

## Event stream

`GET /api/v1/events?session_id=<uuid>` subscribes to one session; omitting the
filter subscribes globally. The web page uses it; the device polls turn
metadata instead. A connection starts with `resync`. Read current
turn metadata and complete message content to establish a baseline. Events include
`session.updated`, `chat.delta`, `chat.reset`, and
`message.updated`. Chat deltas carry a UTF-8 byte offset. A zero offset may be omitted by JSON
encoding. Ignore deltas already included in the snapshot, and refresh the
complete reply after an offset gap or `chat.reset`. `message.updated` reconciles
its status with the full stored snapshot. Heartbeats occur every ten seconds.

Slow consumers are disconnected and reconcile on reconnect. The persisted
metadata and complete message snapshots support recovery after an event gap. SSE delivery itself
is transient. Approval prompts are surfaced as attention states; detailed
approval controls remain a subsequent product decision.

## Spoken replies

`GET /api/v1/sessions/{id}/messages/{message}/speech` streams a reply's voice.
The gateway waits until Hermes has finished the reply, drops Markdown markup,
code blocks and link targets line by line, and synthesizes the whole reply in
one request, so speech has no per-sentence start-up gaps. The response is
framed binary: one kind byte, a little-endian uint16 length, then the payload.
Kind 1 starts speech (uint16 index 0 and the first 240 bytes of the text; the
device shows the reply text it fetched itself), kind 2 carries 16 kHz PCM16LE
mono audio in 128 ms frames, and kind 3 ends the stream with a status byte
(0 done, 1 failed, 2 cancelled, 3 size limit). Kind 4 is an empty keepalive
sent every 5 s while no other frame is ready, so a device's read timeout
survives a long think.

Audio is released once 1 s is ready; synthesis then runs faster than playback
and stays ahead. The stream is cached in the gateway: synthesis starts on the
first request for a reply and is shared, later listeners replay from the
beginning, and `?offset=<bytes>` resumes a dropped connection at any byte of the
cached stream, even mid-frame (keepalives are not part of the cache and are not
counted; none is sent until a resumed connection has reached its offset, since
the device may be inside a frame). Ten seconds after the last listener leaves an unfinished stream,
synthesis stops; a later request redoes it. Finished streams stay replayable
for ten minutes, up to eight streams, and one reply is bounded to 16 MiB of
audio and 8000 bytes of text. `/api/v1/server` reports `speech`.

`VOICE_TTS_PROVIDER` is `openai` (default), `fake` (a soft chime, 100 ms per
character, for development) or `off`. OpenAI deprecated its Speech API models
(`gpt-4o-mini-tts`, `tts-1`, `tts-1-hd`; shutdown 2027-01-06), so the gateway
uses the Realtime API as a reader: `OPENAI_TTS_MODEL` defaults to
`gpt-realtime-2.1-mini` and `OPENAI_TTS_VOICE` to `marin` at
`OPENAI_TTS_URL` (`wss://api.openai.com/v1/realtime`). Each reply is one
out-of-band `response.create` (`conversation: "none"`, empty input) whose
instructions say the text verbatim; 24 kHz output is downsampled to 16 kHz
with a windowed-sinc low-pass. A request fails after 20 s without upstream
events and is bounded to 10 minutes. One configured session is kept warm like the
transcription session. A live check from the deployment host on 2026-11-15
read Chinese, English and mixed sentences verbatim with the first audio after
0.64–0.87 s; opening a session through the proxy took 3.7 s.

## Device diagnostic reports

`POST /api/v1/device/logs` accepts one JSON report of at most 64 KiB from a
terminal, with the device bearer key. It carries firmware version and build,
trigger, boot count, reset reason, uptime, heap figures, up to 1024 events
`[boot, ms, type, a, b, tag]` and up to 16 KiB of warning/error log lines. Malformed
reports return 400. The raw report is stored in SQLite (the newest 100 are
kept) and one warning line with the report ID and a summary is logged.
`GET /api/v1/device/logs` lists report metadata; `GET /api/v1/device/logs/{id}`
renders a Chinese text timeline, with `format=json` returning the raw upload.
Events from the reporting boot get wall-clock times derived from the gateway's
receive time. The web page opens the same list from the sidebar. Reports hold
event codes and log lines only, never audio, transcripts or credentials.
`internal/diag/testdata/report.json` is produced by the firmware encoder and
pins the wire format.

## Tests and build artifacts

`go test -race ./...` covers first-send session creation, send idempotency and
concurrency, side-channel transcription, restart recovery, UTF-8 windows, streaming PCM,
simulated OpenAI WebSocket events, Hermes HTTP/SSE and media filtering.
`tools/smoke-server.sh` starts the real binary with local fake upstreams,
exercises transcription and a first send, and verifies persistence after a restart.

`tools/build-server.sh amd64` and `arm64` produce CGO-free Linux binaries under
`build/server/linux-<arch>/`, with SHA256SUMS. GitHub Actions runs the checks
and uploads both architecture artifacts. A `v*` tag also publishes them as
release assets. Real provider credentials, LAN mDNS,
microphone accuracy/latency and device RAM remain integration acceptance work.

## Web chat validation on 2026-11-15

Go tests passed, including the public asset check for `/ui/icon.svg`. A local
gateway with fake ASR and a fake streaming Hermes was driven in headless
Chromium: key rejection and sign-in, first-send session creation and titling,
streaming with Markdown tables, nested lists and code blocks, a second message,
a device-appended draft appearing in the composer, a staged device recording
showing its live transcript and committed text, Ctrl+K, rename, reload with a
`#/c/` link, remembered key, dark theme, deletion, and a 390-pixel layout
without horizontal scrolling. The deployed gateway still needs a check against
the live Hermes.

## Management validation on 2026-11-13

Server builds passed for the local machine, Linux AMD64 and Linux ARM64. The
static gate passed host tests, Go race tests and the real-binary smoke workflow.
Management tests cover public assets, authenticated session reads, same-origin
deletion and Hermes synchronization through a local simulated upstream. Browser
checks covered login, pagination, question/reply preview, delete-confirmation
cancellation, last-page fallback, empty lists, sign-out and a 390-pixel layout.
The running local server's root page was also verified in the browser.

## Docker Hermes credentials

Hermes may load `API_SERVER_KEY` from its own data-directory `.env` over the
container environment. Configure `HERMES_API_KEY` with the value used by the
running API service. An environment value reported by `docker compose exec`
alone may differ from the profile file loaded by Hermes.

For local development using `server/.env`, explicitly pass
`go run ./cmd/voice-agent-server -env .env`. The default file is `.env.local`.
