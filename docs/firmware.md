<p align="right"><a href="firmware.zh_CN.md">简体中文</a> · <strong>English</strong></p>

# AI Passport firmware

The first device client is under `firmware/`, targeting AI Passport's ESP32-C3,
8 MiB Flash, 240×320 rounded display, three ADC buttons and ES8311 audio path.
It adapts voice-memo's board support, Chinese fonts, settings,
Wi-Fi and BLUFI lifecycle, immediate short-press detection and screen-off policy.
The existing memo checkout remains the hardware reference.

## Voice-first sessions and spoken replies

Opening a session shows the voice view: an orb, one status line, a one-line
strip and two key hints. Recording happens in the same view: the timer takes
the status line and the strip shows the last ten characters of the live
transcript. After release the strip spins while the transcript settles (no key
acts in that moment), then expands into the draft panel, whose header shows
the draft's character count against its 140-character bound (red from 90 %). Sending collapses it
to one line with the question while Hermes thinks: a comet arc orbits the orb,
the bars rise in turn and the status counts the seconds waited; this lasts
until Hermes has finished, since the device fetches the reply whole. If Hermes
reports `attention`, the status reads "等待审批", polling continues, and new
recordings or sends in that session stay disabled until execution resumes. While a
reply is spoken the strip keeps the question, dimmed; while Hermes thinks and
while it speaks the strip is a flat grey field without a border, marking the
question as sent and no longer editable, unlike the bordered resting strip. The
status reads
"正在朗读" with the audio heard so far (such as 0:23; the full length is not
known ahead, so there is no progress bar); when speech ends or is stopped the strip
becomes a vertical ticker over the reply, from its first line: Markdown marks are
dropped, every sentence starts a line, a sentence wider than the strip
continues on further lines, and closing punctuation never starts one. Each
line stays 220 ms per character (1.8–5 s) and the next slides up in 0.25 s;
after the last line it rests 3 s and starts over. Up/down hold and move lines,
the status shows the position, and 15 s without a key resumes the ticker. Notices replace the status line
instead of overlaying other elements. The text card is the previous
conversation page; it shows the reply once it is complete and shows
"正在朗读" while speech plays. While Hermes writes, the device checks only the
turn's state once a second (no SSE) and then downloads the complete reply. Two marks under the header tell the views apart.

When the gateway reports `speech`, the device reads aloud each new newest
exchange of the open session, whether it was sent from the device or from the
web page; opening a session does not replay its last reply. `agent_speech.c`
streams `/messages/{id}/speech` (a frame marking the start of speech, 16 kHz
PCM and keepalives every 5 s while Hermes thinks), writes audio to the ES8311 at
the capture format and publishes whether speech is audible, its loudness for the
orb and the audio played so far.
Recording stops speech before the microphone starts; long Down stops it in
either view once it is audible (while the reply is still being prepared, long
Down leaves the session instead); leaving the session stops it. The I2S DMA ring holds only 90 ms,
so playback is split: the stream reader passes audio straight from the HTTP
reads into a 16 KiB buffer (about 512 ms) shared with recording
(`agent_pcm_pool.c`; the two never run together, since recording ends
playback), and a writer task at the capture task's priority feeds the codec
after 400 ms of prebuffer, refilling 200 ms after running dry, with short fades
around each dry spell so it does not click. Reply audio arrives already
shaped for the speaker by the gateway (high-pass and limiter, see the server
guide). The speech stream is flow-controlled by that buffer, so the TCP
window sits in Wi-Fi RX buffers while playback catches up: the firmware keeps
12 dynamic RX buffers, an A-MPDU window of 6, an 8-segment (11520-byte) TCP
window and SACK. With 6 buffers and a 4-segment window, frames were dropped
and each loss waited 0.24-0.54 s for a retransmission timeout on the gateway,
which outlasted the buffer (measured in November 2026). A dropped connection resumes at the
exact stream byte it stopped at (`?offset=<bytes>&stream=<id>` using the
`X-Speech-Stream` response header, up to three times, keepalives not
counted), with the frame parser carried over. If the gateway would have to
synthesize the stream again (it answers 409 `speech_restarted`), playback ends
there instead of continuing from new audio. The speech trace event records
underruns, the longest network gap and resumes; Wi-Fi modem sleep is held off
while speech plays, and disconnect reasons are traced. The buffer and the
writer are reserved once at boot: allocating them per reply fragmented the heap
(after one spoken reply the largest free block fell from 59 KiB to 35 KiB while
the minimum free heap reached 26.7 KiB).

The resting ticker draws one still line and animates only the 0.25 s slide
between lines, so it costs almost nothing to render. The orb sits 8 px below the mode marks so the thinking arc
never touches the header. With a draft, long Down clears it locally; nothing
is sent.

## Diagnostic reports

The firmware keeps a diagnostic trace in RTC memory (`agent_trace.c`): the last
160 events and about 1.5 KiB of warning/error log lines, 5.3 KiB in total. It
survives software resets, panics and watchdog resets, and is cleared at power-on.
Events cover button press/release, resolved gestures with page and capture
state, page and capture transitions with heap figures, mutating HTTP requests
and failed reads (status and elapsed time, no IDs or bodies), the audio upload
result, the recording result flags, send requests and capture errors. Routine
successful polls are not recorded, so they cannot flush the context.

A report is uploaded to `POST /api/v1/device/logs` after a failed recording, a
capture error, an abnormal reset (panic, watchdog, brownout, lockup) or a draft
send within 5 s of a failed recording. The network task uploads while capture is
idle, at most once per 15 s and 30 times per boot; a failed upload stays pending.
The upload streams chunked JSON from a 5.3 KiB heap snapshot held only during the
request. Reports never contain audio, transcripts or credentials.

## Following messages from other clients

While a conversation shows its newest exchange and that reply has settled, the
sync task reads the session's newest turn metadata every 3 s. When the web page
(or another client) has sent a newer message, the device switches to it, waits
for its reply as described above and reads it aloud. When the newest message is
the one shown but its state or revision has changed (another client retried it
under the same ID), the device reloads that exchange; a retry from another
client is not read aloud again, while one retried on the device is. Reading an older exchange is not interrupted.

## Screens and controls

| Screen | Short press | Long press |
| --- | --- | --- |
| Sessions | Up/down select, crossing a page boundary loads four more sessions. OK opens the selected session. | OK opens a new local chat and records while held; the gateway creates the session on its first send. Up cycles to the next top-level page. Down turns the screen off. |
| Voice (default in a session) | Without a draft: OK opens the text card; the strip shows the last reply one line at a time and advances on its own; the first up/down press holds the line in view, then each press moves one line; past the last line, or after 15 s without a key, it advances on its own again. With a draft: OK sends it; up/down scroll it. On a failed or unconfirmed reply OK retries. | OK records (and interrupts speech). Down leaves the current state: stops audible speech, else clears the draft, else returns to sessions. |
| Text card | Up/down scroll about 70 % of the visible text per press, in whole lines. At an edge a double press (second press within 0.5 s, hinted on the first) opens the previous/next message (input/reply, then neighbouring exchanges), always at its top; at the first or last message it says so. The status shows the exchange position, e.g. 第 3/7 轮. OK returns to the voice view on the newest exchange. | OK records. Down stops audible speech, else returns to sessions. |
| Recording | Release OK to finish. There is no cancel while recording or transcribing. | Keep OK held to record. |
| Settings | Up/down select, OK enters an editor or saves it. Up increases and down decreases a numeric value. | In the settings list, Up cycles to the next top-level page and Down turns the screen off. In an editor, Down cancels the unsaved edit and returns to settings. |
| Provisioning | OK finishes provisioning and restores station networking. | Down finishes provisioning and returns to settings. |

The conversation displays the pending text for review. It lives only in device
RAM (protocol 2, 2026-11-16): transcription is a gateway side channel that
returns the final text and stores nothing. Short OK sends the text shown, with
its draft serial, so a newer recording or a clear cancels a send still queued.
Long OK re-records: a successful transcript replaces the draft, while
cancellation, no speech (`没有听清`), a transcript over 140 characters or a
failed upload keeps the earlier text. Leaving the session discards the draft.
An unsent draft does not delay sleep; deep sleep or a reboot loses it. Each cached input or reply is bounded to
8000 bytes, the gateway's spoken-text bound, so the shown and spoken parts
agree. Longer content keeps a UTF-8 prefix, leaves the rest unread on the server,
and ends with a notice that only the beginning is shown and the web page has it
in full. Sessions and settings share a
registered top-level page cycle. Long Up advances and wraps that cycle. Additional
top-level pages use the same registry. Long Down turns the screen off at the top level and exits
one level from detail screens. Turning the screen off keeps networking, the open
session and any draft; the next key press lights it again and only wakes, except
that holding OK records at once. The deep-sleep delay then runs as after an idle
screen-off, counted from when recording, thinking or speech ends. Gateway
rediscovery is available by confirming the server row in settings. Each short
press is handled independently of the driver's multi-click events.

Settings contain network, server, volume, brightness, screen-off delay,
deep-sleep delay, theme, time zone and version. The sleep delay starts when the display goes dark;
the screen stays on while recording, while Hermes thinks and while a reply is read aloud.
Selected session titles scroll horizontally. Dates and unselected rows have
bounded single-line layouts. Active pages own their widgets and animations.
Switching pages deletes the inactive page after loading a fixed transition screen.

The gateway's index supplies the session list. Long OK on the session list
opens a new chat with a local session ID and the name `新会话`; nothing exists
on the gateway until the first send, which creates the session and returns the
title the gateway derived from the sent text. An abandoned recording therefore
leaves no empty session. Internal IDs remain in protocol fields.
The user authorized deletion of the two existing prototype sessions on
2026-11-14. Both local deletion and Hermes synchronization completed. Their
old title suffix handling and the v1 database migration were removed.

## Visual refresh

The refreshed interface keeps every key interaction. Warm paper-toned light and neutral dark
palettes share a violet accent for selection, progress and icons; failures use
the recording red. Session rows are rounded cards with a title-initial badge,
and today's sessions show only the time. A two-dot indicator under the header
marks the Sessions/Settings root cycle. The light palette avoids pure white,
which glares on the small backlit panel. The recording page shows a large timer,
a status pill, a symmetric three-bar level meter (short outer bars, tall centre)
and a transcript card. Empty states,
provisioning and settings use accent-tinted circular icon badges, and numeric
setting values use large figures with a pill meter. The design adds no shadows,
gradients or translucent layers, and its animations are the two existing
spinners.

On 2026-11-15 the user authorized segmented flashing of `e1bc3c5` (full image
SHA-256 `39b58fd51b3c1def819678183629c5b96bb9c57939a5ec183744e6b510bc1394`,
application 5,991,296 bytes); all three writes verified and NVS was retained.
Application-ready free heap was 82,752 bytes with a 69,632-byte largest block.
A three-minute observation covered two successful recordings (0.9 s and 5.8 s,
both `audio response=200`) without a PCM queue overflow. Minimum free heap was
71,060 bytes and the lowest sampled largest block was 65,536 bytes. The earlier
sixteen-bar build `fcc93bb` overflowed the PCM queue once in two recordings,
which led to the three-bar meter. A five-minute run of nine recordings then
showed the real cause: seven succeeded (up to 57.9 s), while the first recording
in a new session and the first after about 30 s idle overflowed at 2.8-2.9 s.
The gateway did not read the upload until the OpenAI session reported
`session.updated`, about 2.5 s on a cold connection through the proxy, so TCP
backpressure filled the 8 KiB device queue. The gateway now prefetches up to
16 MiB of upload per capture (see the server document). Minimum free heap in
that run was 67,304 bytes with a 65,536-byte lowest largest block.

`tools/ui-preview.sh` renders every screen on the host in both themes with the
locked LVGL version and the production `agent_app.c`, writing PNG frames and
contact sheets under `build/ui-preview/frames/`.

## Compact header and card layout

Page and conversation titles now share one row with local time, battery and an
optional back icon. Titles are centered in an 80 px field and use a single-line
ellipsis. The clock has a separate 44 px field on pages with the back icon.
Conversation cards start at y=34 and end at y=308. Role and outcome status share
the card's first row. The scrollable body is 228 px high, up from 176 px. Status
text is right-aligned alongside the role, and the waiting graphic occupies the
body while generation is active. Four-row session/settings lists use the same
34–308 px vertical region.

Native LVGL ASAN/UBSAN checks cover light/dark rendering, all 1,440 clock values,
non-overlapping header fields, role/status bounds, complete-message reading and
history/retry flows. Stable truncated titles and status messages cause no idle
redraws. Source-text caches handle LVGL DOT mode's edits to its displayed buffer.
The previous `1617a2dc` device evidence belongs to the preceding layout.
Authorized acceptance of the compact layout is recorded below.

Compact-layout firmware `077092ec` passed the complete gate and independent
archive verification. Full-image SHA-256 is `077092ec6bac7942cf0e72c9c95d43cd79a16e08a0874112dc5d0f8b123057d8`.
Matching ELF SHA-256 is `1e18ab20501de072cf4a1a896436b1046ae3a3f7d6feddddd848d0d0faf44f97`.
The bundle is `build/firmware/077092ec6bac7942cf0e72c9c95d43cd79a16e08a0874112dc5d0f8b123057d8/`.
The app is 3,531,376 bytes, static DRAM BSS is 47,944 bytes and initialized DRAM
is 15,252 bytes. The partition table matches `1617a2dc`. After authorization,
segmented flashing through the device serial port verified all three writes and
retained NVS. Startup matched ELF `1e18ab205`, joined Wi-Fi and discovered the
local gateway. Three-minute serial observation covered startup and conversation
viewing without a panic or unexpected reset. Minimum recorded free heap was
67,284 bytes, the lowest sampled largest block was 65,536 bytes, and the final
free heap was 84,892 bytes. The serial port closed normally. The user confirmed
header/status alignment, improved card spacing, animation through completion,
reply opening at the beginning and manual scrolling. These device checks passed.
Native previews use synthetic text and production widgets. Full clock-range, long-title/status stability and ASAN/UBSAN checks
passed. Long replies retain the complete-message reading behavior.

## Appearance and local time

The approved design uses a warm beige light palette and a neutral dark palette, rounded focus rows,
scrollable whole-message cards and a single title/time/battery status row. The theme
setting offers Light, Dark and Auto. Auto uses light from 07:00 inclusive until
19:00 exclusive in the configured local time, and dark otherwise. Until a valid
clock is available it uses dark and displays `--:--`. Existing SNTP synchronization
supplies UTC. Time zone is a fixed UTC offset, adjustable in 15-minute steps
between UTC-12:00 and UTC+14:00. Regional daylight-saving rules are not stored.

Theme and time-zone editors preview changes. OK saves to NVS. Long Down cancels
and restores the confirmed setting. Scheduled changes recolor the existing
widgets, preserving the current card, recording state and editor. Settings show
four rows per viewport. Time, battery values and session dates share the compact
16 px metadata font. The old terminal prefix, header separator and row numbers
have been removed.

## Provisioning page redesign

The user supplied a photo showing text against the old panel edge and a large
separator/footer. The provisioning screen now uses a centered Bluetooth/status
icon, a one-line state title, two compact instruction lines, a separate inset
identifier card and a contextual footer. The identifier is `BLUFI_AGENT` while
pairing and the selected network during connection/success. Long names scroll
within one bounded line. Control bytes in SSIDs display as spaces. Success shows
the IP address, while failure retains its diagnostic error code and retry guidance.
The same layout applies to beige and dark themes. Icons use LVGL primitives.

Firmware `1c3847e1` passed the complete build/host/Go race/vet/smoke gate. Native
LVGL checks exercised all seven states in both themes, long ASCII/Chinese network
names, control-byte handling, refresh stability and screen teardown. Software
render previews were exported using synthetic state and network data.
Full-image SHA-256 is `1c3847e1791aaaaef8050ed022b79807fdecd94ea7541b37c2934f2292f04ad8`. Matching ELF SHA-256 is
`16286e97aa4cb0319cf91c1bb717d97900b8760bd6ff52d3ce2c4ec756b5ba55`. The verified archive is
`build/firmware/<full-image-sha256>/`, with a 3,521,088-byte application.
The user authorized a full erase and merged-image flash of `1c3847e1` on
2026-11-14. Erase, write verification and a 30-second startup observation passed,
with matching ELF identity and no stored Wi-Fi credentials. Application-ready
free heap was 83,856 bytes, with a 73,728-byte largest block before provisioning.
Visual and end-to-end provisioning acceptance remain pending.

A subsequent spacing correction moves the identifier card up by 5 px and the
footer up by 1 px. Pixel measurements of the actual LVGL advertising-state
render show 16 px of clear space above and below the card in both themes.
These previews use synthetic state and network data. Firmware `56906c9a` passed
the complete gate and independent archive verification. Full-image SHA-256 is
`56906c9a3a76e096fb2fa09cb0834cd3bfb7da88fd26ccfd1307899d8eb0bd29`,
with matching ELF SHA-256
`98e55af81036d77dda79400a145c55ded63ca0fa2f2df9cf69d43fbcd7d8f2b1`.
The verified archive is `build/firmware/<full-image-sha256>/`. The user authorized
flashing `56906c9a`; segmented writes through the device serial port on
2026-11-14 verified all three hashes and retained NVS. The 30-second startup
observation matched ELF `98e55af81`, showed application-ready free heap of
83,828 bytes (largest block 73,728 bytes), started BLUFI advertising and observed
a phone connection. No crash appeared, and the monitor closed normally.
Visual spacing and completed Wi-Fi provisioning await user acceptance.

## Recording memory diagnostics

Firmware `e36c9dbf` samples free/minimum free heap, the largest 8-bit block and
input-task stack headroom every five seconds and on page/capture changes. The
PCM worker logs start/stop heap and stack readings, capture errors and queue
backpressure. The network worker logs audio response status and recording
outcome with stack headroom. Samples run in existing tasks. The complete gate
and archive verification passed. Full-image SHA-256 is
`e36c9dbfdbf9d026f587a017f6ef00679e88f07f4f7762a0d038b6b80cf1cd3f`,
with matching ELF SHA-256
`5c7407db00da8b51022b3ad1ff498581705b09c3d7d804537d3e59a080976e36`.
The archive is `build/firmware/<full-image-sha256>/`. Application size is
3,522,416 bytes. The authorized full erase/flash and five-minute observation completed.
Fresh provisioning and discovery succeeded. Recorded memory and the service cleanup
failure are described in the whole-message checkpoint above.

## Fonts and coverage

Chat and session titles use Noto Sans SC Bold at 16 px. The inventory includes
all of GB2312, including its symbol rows, all 20,902 GBK ideographs
U+4E00-U+9FA5 for traditional and rare characters, and the Noto Sans SC glyphs
for Latin-1/Latin Extended-A, Greek, Cyrillic, kana, arrows, math operators,
enclosed numerals, box drawing, geometric and miscellaneous symbols and fullwidth
forms, for 22,668 emitted code points. Temperatures such as `12°C` and `℃` render
directly. A 16 px monochrome Noto Emoji 3.002 font supplies 1,353 emoji absent
from Noto Sans SC as the chat font's fallback. Variation selectors, zero-width
joiners, keycap marks and skin-tone modifiers are removed before display, so a
joined sequence shows its component emoji. Chinese curly double and single quotes
(U+2018/U+2019/U+201C/U+201D), en/em dashes, ellipsis and common typographic marks
are included in both the chat and 20 px UI inventories. The reported missing
character U+6E25 is covered. The 20 px Regular UI font falls back to this expanded
chat font. Characters outside the supported inventory render as `?`.

Dates and battery percentages use Noto Sans SC Regular at 16 px, with a compact
20 px ASCII line box. The battery field is 48 px wide to fit proportional `100%`.
List metadata ends at row-local y=53 within the existing four-row layout.
Font bitmaps and lookup tables are constant Flash resources.

`firmware/assets/fonts/manifest.json` records each output's character inventory,
source hash, converter version and payload hash. The OFL license and source
provenance accompany the assets. Reproduce the maintained payloads using the
pinned converter and licensed source files.

```sh
python3 tools/generate_memo_fonts.py --check --fetch \
  --source-dir /path/to/font-sources \
  --converter /path/to/lv_font_conv
```

The expanded inventories add about 1.6 MB of constant Flash and no RAM.

Host checks cover both GB2312 Chinese levels, the GBK ideograph range, common
reply symbols, emoji fallback separation, generated payload hashes, fixed UI
strings and the proportional battery width. Native LVGL checks also exercise the
real glyph descriptors, U+6E25 rendering, the 20 px fallback and 40-line partial
rendering. Owned whole-message snapshots preserve and render curly quotations
in both themes. The punctuation extension preserves every previously emitted
glyph's advance, bounding box and offsets, together with both font line metrics.
The user confirmed the reported missing character renders correctly,
and accepted the revised chat spacing on `04c3dd73`.

## Audio and memory

Recording streams mono PCM16 at 16 kHz over chunked HTTP to Go, which owns
OpenAI transcription, provider credentials and upstream TLS/WebSocket connections.
The device keeps a 16 KiB PCM buffer shared with spoken-reply playback, four
session metadata records, a 512-byte live transcription preview and the draft
itself, allocated to its size (at most 560 bytes for 140 characters).

The selected exchange owns one complete user input and one complete Hermes reply
in RAM, allocated according to their actual byte lengths. History selection loads
another whole exchange and releases the previous cache. Message content stays in
server storage. The visible LVGL label owns a separate filtered display snapshot,
replaced and freed under the LVGL lock; the old snapshot is freed before the next
is copied, so a reply is never held twice beside the cache. The voice page strips
its ticker text in place from one copy and trims it to the 256 lines the ticker
can reach (about 7.5 KiB), and the question strip keeps only
the input's first 255 bytes in a static buffer. If the new snapshot cannot be
allocated, the label stays empty and the notice reports memory pressure. Cache buffers are released before BLE startup.

The device does not use SSE. While a reply is being written, the sync task reads
only the turn metadata once a second; once the state settles it fetches the
complete reply in one read. Unchanged card background colors are not reapplied,
avoiding continuous invalidation of a static message. Repeated transport failure
invalidates the discovered endpoint so the gateway can be found again.

Whole messages scroll locally. A left back chevron and centered, truncated title
share the compact header with time and battery. The assistant waiting state draws
a lightweight spinning arc throughout generation. Completion
reveals the entire reply at its beginning, with up/down input controlling reading.
While waiting, Up can select the user input. Failed or interrupted replies reveal
retained text and their outcome status. Draft send and reply wait have explicit
state labels.

The device requires gateway protocol 2 with `bounded_paging`, `text_tail` and
`whole_messages`, and uses `speech` when the gateway reports it. Discovery remains `_voiceagent._tcp.local`; BLUFI advertises as
`BLUFI_AGENT`. `CONFIG_AGENT_API_KEY` configures the optional gateway credential.

## Build and validation

Activate ESP-IDF 5.5.3, then run from the repository root.

```sh
./tools/validate.sh --static
./tools/validate.sh --firmware
# Both stages
./tools/validate.sh
```

The firmware gate generates an isolated configuration from tracked defaults,
builds and merges the image, verifies the partition/image layout and archives
matching ELF, MAP, component images and `flash_args` under
`build/firmware/<full-image-sha256>/`. The verified merged output is
`build/Voice-Agent-Terminal-full.bin`. Host tests exercise JSON limits, UTF-8,
four-entry paging, rapid short presses, hold/release suppression, persistent
settings and idle timing. Font checks cover fixed Chinese UI strings.
The CJK subset replaces unsupported dynamic characters with `?`.

The partition layout is NVS at `0x9000` (24 KiB), PHY at `0xF000` (4 KiB) and
factory application at `0x10000` (the remaining Flash). Before migrating a device,
finish uploading pending voice-memo recordings through its existing firmware.
Segmented flashing writes bootloader, partition table and application at their
recorded offsets. Existing Wi-Fi/settings use the established NVS locations.
The merged image starts at `0x0` and its padding can reset NVS/PHY data.
Flashing requires approval for the verified build and its data impact.

Dated build and device validation records are in the
[firmware validation history](firmware-history.md).
