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

## Discovery and limits

Go publishes `_voiceagent._tcp.local` with `ver=1`, `path=/api/v1`, capabilities
and an IPv4 fallback. Loopback listeners skip mDNS. An advertisement failure is
logged while HTTP remains available. The device validates `/api/v1/ping` and
reads `/api/v1/server` after discovery.

- Session, segment and message IDs use lowercase UUID syntax.
- Metadata pages default to 4 entries and accept `limit=1..20` and an offset.
- Drafts hold at most 64 segments and 64 KiB of text; joining inserts blank lines.
- Text reads use UTF-8 byte offsets and windows up to 2048 bytes.
- Each device SSE frame is at most 1024 bytes. Delta text uses at most 64 bytes.
- Each filtered reply holds up to 2 MiB. Sanitized upstream SSE events have a
  256 KiB bound. Oversized ordinary events produce an explicit unconfirmed outcome.
- Up to four audio uploads and 64 SSE subscribers run concurrently. Capture
  timeout and audio duration are bounded to five minutes; chat timeout is ten.

## Sessions

`POST /api/v1/sessions` accepts `{"id":"<uuid>","title":"Ideas"}`.
Creation is persisted before contacting Hermes. A ready session returns 201;
an unsettled creation returns 202 with `state: "creating"`. Background recovery
retries the same upstream ID. Hermes 409 responses are reconciled against the
existing API session. Session titles are device labels stored in Go.

`GET /api/v1/sessions` lists the local session index. `GET` and `PATCH` on
`/api/v1/sessions/{id}` read metadata and update the local title.
`DELETE` marks a session as deleting, coordinates upstream deletion and retains
a retired local ID. Active or unconfirmed messages return a busy response.
An upstream session's API source is verified before deletion.

## Drafts and audio

`GET /api/v1/sessions/{id}/draft` returns one consistent draft revision and a
metadata page. Each item includes segment identity, state, size and a preview.

`POST /api/v1/sessions/{id}/segments` appends
`{"id":"<segment-uuid>","revision":1,"text":"first thought"}`.
The response includes the new draft revision. An empty text creates a recording
segment. `PATCH` on `/segments/{segment}` accepts `revision` and replacement
`text`. `DELETE` accepts the reviewed `revision`. Edits retire the old recognition
generation and cancel its active capture.

`POST /api/v1/sessions/{id}/segments/{segment}/audio?revision=<n>&sample_rate=16000`
streams raw mono little-endian PCM16 with `Content-Type: audio/pcm`.
Supported input rates are 16000 and 24000. The request can be chunked; EOF commits
the audio turn. Go incrementally resamples 16 kHz input to 24 kHz and connects
to OpenAI. A successful response means recognition finalized in the draft.
Partial text is persisted during upload and announced by `draft.updated` events.
Audio bytes are transient; draft text persists. Encoded audio intake can be added
when the device transport is selected.

`GET /api/v1/sessions/{id}/segments/{segment}/text` reads a text window.
Use `offset`, `limit` and optionally `revision`. A changed requested revision
returns 409. Recognition, edits and send each advance the draft revision.

## Explicit send and replies

`POST /api/v1/sessions/{id}/messages` accepts
`{"id":"<message-uuid>","revision":<reviewed-draft-revision>}`.
One transaction checks the revision, joins the current segments, persists an
immutable message and retires those segments from the draft. It returns 202.
Repeating the same ID and reviewed revision returns the same logical message.
A stale revision returns 409 so the user can review the current draft.

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
A changed endpoint or credential prevents submission under a new identity scope.
This recovery targets Hermes' canonical durable-admission mode; verify the
deployed mode's idempotency behavior before live use. Legacy streaming retries
have different guarantees.

## Device event stream

`GET /api/v1/events?session_id=<uuid>` subscribes to one session; omitting the
filter subscribes globally. A connection starts with `resync`. Read current
metadata and text windows to establish a baseline. Events include
`session.updated`, `draft.updated`, `chat.delta`, `chat.reset`, and
`message.updated`. Chat deltas carry a byte offset. A reset asks the client to
reload the reply window. Heartbeats occur every ten seconds.

Slow consumers are disconnected and reconcile on reconnect. The persisted
metadata and windows support recovery after an event gap. SSE delivery itself
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
and uploads both architecture artifacts. Real provider credentials, LAN mDNS,
microphone accuracy/latency and device RAM remain integration acceptance work.
