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
| `VOICE_ASR_PROVIDER` | `openai` by default; `fake` for offline transcription tests |
| `OPENAI_API_KEY` | OpenAI credential, held by Go |
| `OPENAI_TRANSCRIBE_MODEL` | Default `gpt-live-transcribe` |
| `OPENAI_REALTIME_URL` | Default `wss://api.openai.com/v1/realtime?intent=transcription` |

Local data and environment files are ignored by Git. Bind network exposure and
device credentials to the intended LAN deployment. A configured Hermes API
server provides the matching `HERMES_API_KEY` value and API-created sessions.

## Session management page

Open the server root, such as `http://localhost:8080/`, to manage the device
session index. The embedded page provides 20-entry list pages, chronological
complete question/reply previews, and deletion with confirmation. Narrow screens switch
between the list and detail views. The assets are compiled into the Go binary
using the standard library's `embed` and `net/http` support.

If `VOICE_API_KEY` is configured, enter that gateway key on the page. The public
HTML/CSS/JavaScript shell serves the interface assets. API requests use bearer
authentication and same-origin mutation protection. The page keeps
the entered key in memory for the open page lifetime.

Deletion uses the existing session API and synchronizes the corresponding
Hermes session. A pending deletion remains visible until synchronization
completes. Active or unconfirmed messages produce the existing busy response.
The page lists sessions managed by this gateway.

## Discovery and limits

Go publishes `_voiceagent._tcp.local` with `ver=1`, `path=/api/v1`, capabilities
and an IPv4 fallback. Loopback listeners skip mDNS. An advertisement failure is
logged while HTTP remains available. The device validates `/api/v1/ping` and
reads `/api/v1/server` after discovery.

- Session, segment and message IDs use lowercase UUID syntax.
- Metadata pages default to 4 entries and accept `limit=1..20` and an offset.
  `has_more` reports whether another page exists without an empty-page probe. Drafts also include a transaction-consistent
  `total` segment count so the client can locate the last page directly.
- Drafts hold at most 64 segments. Both aggregate raw segment text and the final
  joined text, including inserted blank lines, are bounded to 64 KiB. Add, edit
  and transcription updates roll back when either budget would be exceeded.
  A pending replacement has a separate 64 KiB text budget.
- Live-transcription and optional window reads use UTF-8 byte offsets and windows
  up to 2048 bytes. Complete-message content responses carry the full stored body.
- Each device SSE frame is at most 1024 bytes. Delta text uses at most 64 bytes.
- Each filtered reply holds up to 2 MiB. Sanitized upstream SSE events have a
  256 KiB bound. Oversized ordinary events produce an explicit unconfirmed outcome.
- Up to four audio uploads and 64 SSE subscribers run concurrently. Audio
  duration is bounded to five minutes. The complete upload/recognition
  request has a six-minute budget for setup, drain and finalization. Chat timeout
  is ten minutes.

## Sessions

`POST /api/v1/sessions` accepts `{"id":"<uuid>","title":"Ideas"}`.
Creation is persisted before contacting Hermes. A ready session returns 201;
an unsettled creation returns 202 with `state: "creating"`. Background recovery
retries the same upstream ID. Hermes 409 responses are reconciled against the
existing API session. Go stores session titles and forwards them when creating Hermes sessions.
If Hermes rejects a title with `400 invalid_title`, creation retries once without
the title using the same session ID. The device keeps its local title while
Hermes assigns its default title. Other creation errors retain the pending state.
Renaming verifies upstream ownership and updates Hermes before the local title.
An upstream failure retains the previous local title for an explicit retry.
The index covers sessions created through Go.
Draft creation and transcription remain available while a session is creating.
Sending waits for the upstream session to become ready. This also supports local
draft development with fake ASR and upstream credentials left empty.

`GET /api/v1/sessions` lists the local session index. `GET` and `PATCH` on
`/api/v1/sessions/{id}` read metadata and update the Hermes and local title.
`DELETE` marks a session as deleting, coordinates upstream deletion and retains
a retired local ID. Active or unconfirmed messages return a busy response.
An upstream session's API source is verified before deletion.

## Drafts and audio

`GET /api/v1/sessions/{id}/draft` returns one consistent draft revision and a
metadata page. Each item includes segment identity, state, size and a preview.
`GET /api/v1/sessions/{id}/draft/text` reads the joined draft text and its
transaction-consistent revision through the same bounded UTF-8 window API.
Its ordering and blank-line joining match the immutable send snapshot.

`POST /api/v1/sessions/{id}/segments` appends
`{"id":"<segment-uuid>","revision":1,"text":"first thought"}`.
The response includes the new draft revision. An empty text creates a recording
segment. `PATCH` on `/segments/{segment}` accepts `revision` and replacement
`text`. `DELETE` accepts the reviewed `revision`. Edits retire the old recognition
generation and cancel its active capture.

For staged re-recording, create the segment with
`{"id":"<segment-uuid>","revision":1,"replace":true}` and empty text. This reserves
one temporary replacement slot, including when the ordinary 64-segment draft is
full. Retry the same ID and creation revision after a lost response. Upload with
`replace=1&defer_commit=1`, then commit or abort that same ID. Mutating or sending
the old draft waits for this operation. Unstarted reservations expire after one
minute and are retired on restart. Draft metadata may temporarily contain 65
entries while the 64-segment ordinary append limit remains enforced.

`POST /api/v1/sessions/{id}/segments/{segment}/audio?revision=<n>&sample_rate=16000`
streams raw mono little-endian PCM16 with `Content-Type: audio/pcm`.
Supported input rates are 16000 and 24000. The request can be chunked; EOF commits
the audio turn. Go incrementally resamples 16 kHz input to 24 kHz and connects
to OpenAI. A successful response means recognition finalized in the draft.
Partial text is persisted during upload and announced by `draft.updated` events.
Audio bytes are transient; draft text persists.

Add `replace=1` to the audio upload to re-record the pending draft. A successful,
nonempty final transcript retires earlier pending segments in the same
transaction that stores the final text. Until then, earlier text remains stored.
On capture failure, a replacement segment is retired when an earlier draft
exists. `replace=0` or an omitted selector keeps the append behavior. The device
uses staged replacement for its direct-send interaction.

With `replace=1&defer_commit=1`, audio completion returns
`{"status":"ready","revision":<n>}` and stores the segment as `staged`.
Each staged operation uses a newly created empty segment ID. The old draft
remains the reviewable text. The device automatically calls
`POST /api/v1/sessions/{id}/segments/{segment}/commit` with `{"revision":<n>}`
to atomically replace it. An identical retry returns the original output revision.
`POST /api/v1/sessions/{id}/segments/{segment}/abort` needs no body and returns
`{"status":"cancelled"|"committed","revision":<n>}`. A committed result is
reported without being removed. Commit and abort compete within one transaction.
Pending replacement locks other edits/captures in the session. Sending waits for
capture and confirmation to settle.

Uncommitted staged text is excluded from the joined draft and discarded after a
restart or expiry. A separate cleanup worker checks every 10 seconds for staged
results ready for at least 60 seconds, with up to 64 sessions per pass. Append
mode preserves its failed partial transcripts for review. Recording modes and
commit identities are persisted in SQLite schema v2. New stores are created directly at v2. The prototype v1 migration was removed
after the user authorized deleting old sessions. Unsupported schema versions
are rejected explicitly.

`GET /api/v1/sessions/{id}/segments/{segment}/text` reads a text window.
Use `offset`, `limit` and optionally `revision`. A changed requested revision
returns 409. Recognition, edits and send each advance the draft revision.
Text windows also accept `before=<UTF-8 byte boundary>` for the preceding window
and `tail=1` for the latest text. These selectors are exclusive with `offset`.
Server 0.2.0 advertises `bounded_paging`, `text_before`, `text_tail` and
`session_titles`, `draft_text`, `replace_recording`, `staged_recording` and
`replacement_reservation`. The device verifies its
required capabilities before connecting.

## Complete messages

The device and management page select one complete user/assistant exchange with
`GET /api/v1/sessions/{id}/turn`. The response contains `message` metadata and
`before`/`after` message IDs. A session with no sent messages returns
`{"message":null,"before":"","after":""}`. Select one historical exchange with
exactly one `message=<uuid>`, `before=<uuid>` or `after=<uuid>` query. Neighbor
selection is scoped to the current session and returns 404 at either boundary.
Malformed or repeated selectors return 400.

Read complete UTF-8 bodies from
`GET /api/v1/sessions/{id}/messages/{message}/content?part=input|reply`.
The plain-text response includes exact byte `Content-Length`, `X-Revision`,
`X-Message-State`, and `Cache-Control: no-store`. Body text retains original
newlines and formatting. Whole-message responses carry no display-page offsets
or page counts. Complete reviewed draft text is available at
`GET /api/v1/sessions/{id}/draft/content`, with its draft revision in `X-Revision`.

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
`{"id":"<message-uuid>","revision":<reviewed-draft-revision>}`.
One transaction checks the revision, joins the current segments, persists an
immutable message and retires those segments from the draft. It returns 202.
Repeating the same ID and reviewed revision returns the same logical message.
A stale revision returns 409 so the user can review the current draft.
Sending while a draft segment is actively capturing returns a busy response.

Workers send one new user message to Hermes Chat Completions with its session ID
and a stable idempotency key. The stored model, input and upstream identity hash
remain associated with the message. Concurrent sends within one session are
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

## Device event stream

`GET /api/v1/events?session_id=<uuid>` subscribes to one session; omitting the
filter subscribes globally. A connection starts with `resync`. Read current
turn metadata and complete message content to establish a baseline. Events include
`session.updated`, `draft.updated`, `chat.delta`, `chat.reset`, and
`message.updated`. Chat deltas carry a UTF-8 byte offset. A zero offset may be omitted by JSON
encoding. Ignore deltas already included in the snapshot, and refresh the
complete reply after an offset gap or `chat.reset`. `message.updated` reconciles
its status with the full stored snapshot. Heartbeats occur every ten seconds.

Slow consumers are disconnected and reconcile on reconnect. The persisted
metadata and complete message snapshots support recovery after an event gap. SSE delivery itself
is transient. Approval prompts are surfaced as attention states; detailed
approval controls remain a subsequent product decision.

## Tests and build artifacts

`go test -race ./...` covers SQLite draft/send transitions, concurrency,
recognition retirement, restart recovery, UTF-8 windows, streaming PCM,
simulated OpenAI WebSocket events, Hermes HTTP/SSE and media filtering.
`tools/smoke-server.sh` starts the real binary with local fake upstreams,
exercises editing and sending, and verifies persistence after a restart.

`tools/build-server.sh amd64` and `arm64` produce CGO-free Linux binaries under
`build/server/linux-<arch>/`, with SHA256SUMS. GitHub Actions runs the checks
and uploads both architecture artifacts. A `v*` tag also publishes them as
release assets. Real provider credentials, LAN mDNS,
microphone accuracy/latency and device RAM remain integration acceptance work.

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
