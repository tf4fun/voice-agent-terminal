<p align="right">
  <a href="roadmap.zh_CN.md">简体中文</a> · <strong>English</strong>
</p>

# Milestones

## Current increment

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
| M2 Drafts and sessions | Persistent draft segments, revision checks, paginated session index, Hermes session mapping | Append/delete/reconnect work; late ASR updates respect segment lifetime; sending commits the displayed revision |
| M3 OpenAI transcription | Incremental audio intake, upstream realtime session, transcript events | Representative Chinese audio produces partial and final text; segment identities remain correct; backpressure stays bounded |
| M4 Hermes chat | Explicit draft submission, session continuation, filtered SSE and complete-message recovery | One reviewed draft produces one logical message; full snapshots, offset deduplication, exact retries, media filtering and reconnects are exercised |
| M5 AI Passport client | Discovery, recording, direct draft review/send, whole input/reply RAM cache, local scrolling, completed reply display and thinking animation | Complete-message journeys and waiting/manual reading behavior pass on the device; measured heap, largest block and stack margins support capture and reply delivery |
| M6 Integrated validation | Repeated full interactions and recovery tests | Reboot during draft/send, upstream loss, slow links, duplicate requests and long replies have observable outcomes |

## Decisions to resolve through implementation

- Measure the current PCM16/HTTP capture together with whole-message RAM growth and SSE recovery on the board.
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
