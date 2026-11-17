<p align="right">
  <a href="roadmap.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Milestones

## Current increment

Protocol 2 (2026-11-16) makes voice input equivalent to typing. Transcription is
a gateway side channel that returns text and stores nothing; the draft stays on
the composing client (device RAM, bounded to 140 characters with a visible
count; web page memory). A send carries the complete text, and a new session is
created and titled by its first send, so no empty sessions arise and neither the
gateway nor Hermes needs to reclaim any. Drafts no longer synchronize between
the device and the web page. An unsent device draft does not delay sleep; deep
sleep or a reboot loses it. Gateway SQLite moved to schema v4 without
migration (v5 since review issue #3, below). Server tests, firmware host tests and the UI preview cover it. On the
device (d6eb351) re-recording, first-send session creation and spoken replies
passed; a 68 KB reply left 12.5 KB minimum free heap with the text card open.
Text therefore has one hard bound per stage (see
[architecture](architecture.md#bounded-content-delivery)): a default Hermes
system prompt asks for replies within 300 characters, and the device caches at
most 8000 bytes of an input or reply, the gateway's spoken-text bound. Device
acceptance of that change, the 140-character limit and draft clearing is NOT RUN.

Review issue #3 fixes: a cancel accepted after the transcript arrives keeps the
earlier draft (the cancel check, replacement and end of capture share one
critical section); each message stores its system prompt, so a retry repeats
the original request (schema v5, no migration); a session still `creating` can
be deleted with its unsent first message; and the gateway holds at most 16
transcriptions. Server tests and firmware host tests cover each; device
acceptance of the firmware change is NOT RUN.

Review issue #4 fixes: the web page clears the composer when a send starts and
never touches it on the response, so text typed meanwhile stays (a failed send
puts its text back); reconnecting refetches a reply whose revision changed even
in the same state; the device reloads the newest message when another client
retried it under the same ID; and a speech resume whose stream would have to be
synthesized again is refused with 409, ending playback. The old session-level
conversation window endpoint (`/api/v1/sessions/{id}/conversation`,
`conversation_windows`), `/api/v1/ping` and unused firmware commands were removed
(message and transcription `/text` window routes remain), discovery now
advertises protocol 2, and turn polling reads metadata only. `tools/test-web.mjs`
runs the web page's regressions in Node. Concurrent transcription preparation
is left unbounded on purpose: one device prepares at a time. Device acceptance
of the firmware changes is NOT RUN.

Review issue #5 fixes: the web client guards late session metadata fetches against
the active thread sequence and session ID, coalesces and revision-guards
concurrent reply reads, preserves failed or uncertain (`TypeError`) sends
independently of any newly typed draft for retry or recovery, and treats
`attention` as active (blocking sends and showing the approval prompt). Speech
streams bind a per-stream `X-Speech-Stream` identity checked on `?offset=&stream=`
resumes across retries, same-attempt re-synthesis, eviction and restarts;
spoken-reply formatting cleans complete Markdown structures before pauses;
background pending-session recovery rotates a keyset cursor (`LIMIT 16` per
round) without starving later entries; ASR and TTS share the warm WebSocket pool
lifecycle in `internal/realtime`; oversized transcripts return `413
limit_exceeded` rather than `502 transcription_failed`; unused helpers in
`memo_browse.h` were removed; and the firmware voice view displays `等待审批`
for `attention` while continuing to poll and blocking new sends. Server tests,
firmware host tests and `tools/test-web.mjs` cover each fix; device acceptance
of the firmware changes is NOT RUN. A follow-up bounded the new Markdown cleaning
(it rescanned the rest of the reply for every unmatched bracket: 19 s for 256 KiB
of `[`) to the first 64 KiB of a reply and 1 KiB per link or code span, removed
the per-sentence splitter that only tests still used, and stopped the device's
`X-Speech-Stream` header callback from reading a pointer into a returned stack
frame on later body events.

Reply speech dropouts and distortion: a gateway TCP trace showed the device
stalling for 0.3–0.7 s while losses waited for retransmission timeouts, so the
firmware now has 12 dynamic Wi-Fi RX buffers, a 6-frame block-ack window, an
11,520-byte TCP window and SACK. Distortion at 100% volume came from the ES8311
adding +3.6 dB; the gateway now applies a 200 Hz high-pass and a look-ahead
limiter (`VOICE_SPEECH_*`, default ceiling -10 dBFS, set by listening).
Firmware `cae95e4e` was flashed on 2026-11-17: 8 replies had 0 underruns and
0 resumes, minimum free heap 31,184 bytes; the user judged -10 dBFS at 100%
volume clear and just short of crackling.

Sessions open on a voice-first view (orb, status line, one-line strip and key
hints) with the text card as a second view; the firmware guide describes it.
The gateway synthesizes each completed reply in one OpenAI Realtime request and
streams it as framed 16 kHz PCM, which the device plays through a buffer shared
with recording and resumes by byte offset after a dropped connection. The device
no longer subscribes to SSE: it polls the turn state once a second while Hermes
writes and then fetches the complete reply; the web chat page keeps SSE. The
device keeps an RTC diagnostic trace and uploads reports after failures, which
the gateway stores and renders. Server tests, firmware host tests and the host
UI preview cover this increment. Firmware `e1bc3c5` was flashed and measured on
2026-11-15 (see the firmware guide); device acceptance of the voice view and
spoken replies on the newest build needs its own evidence.

## Complete-message increment

This increment predates protocol 2: its device SSE subscription was later
removed (the device now polls the turn once a second, see above).

The agreed device model keeps one complete user input and one complete Hermes
reply in RAM. The gateway returns complete message bodies and neighboring
exchange IDs. History navigation loads another whole exchange on demand.
Messages use a locally scrollable card. SSE appends update the cached reply in
the background while the card shows a status graphic and animation. Completion
reveals the whole reply from its beginning, and up/down input controls reading.
The compact header combines title, time and battery, with role/status sharing
the card's first row. The user can review their input while waiting. Failure or interruption reveals
retained text together with the outcome status.

SSE recovery establishes the subscription before reading a full snapshot.
Initial `resync`, byte-offset deduplication, gap detection and `chat.reset`
reconcile the cached complete message. The fixed server-calculated screen-card
profile has been retired. The selected-message cache and device event stream
require memory and interaction measurements on the new firmware.

Server build, vet/race tests, complete-content/history/reconnect tests and the
isolated binary smoke passed for this increment. Firmware `eb3c2632` was
subsequently authorized and flashed, with normal startup and discovery. The user reported slow SSE presentation and missing curly quotes.
Follow-up fixes reduce device read batching, remove static-card redraws and add
punctuation coverage. A temporary live chat located most first-text waiting in
Hermes. Timings, build identities and acceptance scope are in the firmware guide.
The authorized `9b06632b` flash passed startup/discovery and a recording/reply
workload without an unexpected reset. Minimum free heap was 70,608 bytes. The
user confirmed improved SSE appending and correct Chinese quotation marks.
The subsequent reading refinement keeps the animation visible through
generation and reveals the reply after completion. Long-message stress and reconnect recovery remain open. Compact-layout firmware `077092ec` passed the
complete gate, native LVGL checks and authorized segmented flashing. The user
confirmed header/status alignment, card spacing, waiting through completion,
reply starting at the top and manual scrolling. Three-minute startup/viewing
observation reported a 67,284-byte minimum free heap and no unexpected reset.

Three subsequent independent full-project reviews ran sequentially. Round 1
fixed two recording/reservation boundary defects, round 2 fixed capture-generation
cancellation/registration races, and round 3 confirmed no new actionable defect.
Reviewed firmware `28aab2eb` and its matching native gateway passed the final
complete gate and independent firmware/archive checks. The user subsequently authorized updating the local server and flashing
`28aab2eb`. Startup/discovery and server capability checks passed, with a
73,348-byte minimum free heap in about two minutes of idle observation. The user
deferred re-record/send/reply testing. The earlier `077092ec` acceptance covers
the UI baseline, and the runtime fixes await functional device acceptance.

## Historical validation

M1–M4 have server implementations and offline acceptance tests. OpenAI realtime
transcription and two-turn Hermes 0.23.1 session continuation passed live testing
on 2026-11-13. M5 has an initial AI Passport firmware implementation with sessions,
recording, drafts and replies. M6 includes server race, restart and binary smoke
tests plus device protocol, settings and gesture host tests. On-device LAN
discovery, recording, append/transcription, send/reply display and settings toggling
passed on 2026-11-13 with firmware `697a36c6`. Firmware `06d4d2d5` subsequently
passed on-device Long Down exit, re-record replacement and direct-send/reply tests.
Firmware `9cdc158a` passed top-level page cycling, detail exit, sleep/wake,
chronological user/assistant display, improved paging and record/send/reply tests.
Firmware `5805f22d` passed the reported missing-glyph fix and consistent prompt
styling. Firmware `04c3dd73` passed bounded history scrolling, top-gap removal,
compact body lines and increased role/body separation on the device.
After three parallel review tracks, `0838950e` fixes cancellation/save boundaries,
text races, replacement recovery/limits and discovery candidate rotation. Host,
server and complete build checks passed. Device integration of the paired staged
protocol and schema v2 awaits acceptance.
Three subsequent sequential full-project reviews are complete. Round 1 fixed
three server defects plus a supplementary startup issue. Round 2 fixed three
client/media-filter defects. Round 3 confirmed no further actionable defects.
The final `54c86d78` passed the complete validation gate, and the matching local
gateway update and SQLite v2 migration completed on 2026-11-14. Device acceptance
for this build is pending. Audio/memory margins and live fault recovery remain open.
An earlier long-reply report prompted fixed, seven-line message cards with
server-calculated page totals and a four-page device cache. The user approved
modern light/dark styling with Auto mode and configurable local time zone.
Prototype sessions were deleted locally and in Hermes. Old title suffixes and
v1 data-migration paths were removed. These historical records describe the
preceding card-based design. The current increment uses the complete-message
model described above.

| Milestone | Deliverable | Acceptance |
| --- | --- | --- |
| M1 Gateway and protocol | Go service, configuration, mDNS, capabilities, whole-message API and bounded SSE | Discovery reaches the advertised service; complete body, neighbor and recovery contracts are documented; startup and shutdown are clean |
| M2 Drafts and sessions | Client-held drafts, first-send session creation, paginated session index, Hermes session mapping | No session exists without a message; a lost send response is retried without duplication; the sent text is the text shown |
| M3 OpenAI transcription | Incremental audio intake, upstream realtime session, side-channel live and final text | Representative Chinese audio produces partial and final text; nothing is persisted; backpressure stays bounded |
| M4 Hermes chat | Explicit draft submission, session continuation, filtered SSE and complete-message recovery | One reviewed draft produces one logical message; full snapshots, offset deduplication, exact retries, media filtering and reconnects are exercised |
| M5 AI Passport client | Discovery, recording, direct draft review/send, whole input/reply RAM cache, local scrolling, completed reply display and thinking animation | Complete-message journeys and waiting/manual reading behavior pass on the device; measured heap, largest block and stack margins support capture and reply delivery |
| M6 Integrated validation | Repeated full interactions and recovery tests | Reboot during draft/send, upstream loss, slow links, duplicate requests and long replies have observable outcomes |

## Decisions to resolve through implementation

- Measure the current PCM16/HTTP capture together with whole-message RAM growth and spoken-reply playback on the board.
- Refine draft editing and the implemented button mappings through device acceptance.
- Validate the implemented API limits and session page size against the device.
- Verify cache release on history/session changes and measure free heap, largest block and stack headroom for representative full messages.
- Presentation of pending approvals and unavailable media.
- Provider permissions, latency and accuracy using the supplied credentials.

## Validation records

Record gateway builds/tests, provider integration tests and device tests
separately. Device measurements identify the board, firmware build and workload.
Successful host checks establish software behavior under those checks; on-device
interaction, audio, memory and timing require their own evidence.
