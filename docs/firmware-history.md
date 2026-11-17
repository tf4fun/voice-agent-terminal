<p align="right"><a href="firmware-history.zh_CN.md">简体中文</a> · <strong>English</strong></p>

# Firmware validation history

Dated build, review and device validation records for the AI Passport firmware,
moved out of the [firmware guide](firmware.md), which describes current
behavior. Records are kept as written at the time: capabilities, layouts and
behaviors they mention may since have been replaced (for example the protocol 1
`staged_recording` capability and device SSE). Current status is in the
[roadmap](roadmap.md).

## Whole-message and SSE validation checkpoint

The complete-message follow-up replaces the earlier device card-page profile.
Host regressions cover 14 KiB input and 15 KiB reply, raw content integrity,
fragmented SSE and offset replay, stale draft responses after send, draft reopening,
cache-hit metadata restoration and the existing capture cancellation/commit cases.
Native LVGL ASAN/UBSAN checks cover a message exceeding 10 KiB, local scrolling,
stream following/manual reading, role/history navigation, waiting animation,
empty sessions, theme changes and repeated snapshot teardown. Independent agents
reviewed the server, interface and client lifecycle/recovery paths.

Firmware `eb3c2632` passed the complete repository gate and independent archive
verification. Full-image SHA-256 is `eb3c26326c67739fd616456cb9c8e0199f2eb256da5103196919b6380c6329f1`.
Matching ELF SHA-256 is `f07fd38cff152a8d35567c06aa41b955b73081a8f98d65aeaa0b5596ffb1c4ca`.
The verified bundle is `build/firmware/eb3c26326c67739fd616456cb9c8e0199f2eb256da5103196919b6380c6329f1/`.
Application size is 3,527,456 bytes. Static DRAM BSS is 47,656 bytes, down 3,160
bytes from diagnostic build `e36c9dbf`. The cached whole messages and visible
snapshot use dynamic heap, whose new workload peak awaits device measurement.
The matching native Go server is `build/server/whole-message/voice-agent-server`,
SHA-256 `e67e6c3284e04d6e6f937d17b7ef80094af3971b6fc376a50869f2d695883b3b`. It passed vet, race tests,
binary smoke and management-script syntax checks. Both UI and client ASAN/UBSAN
checks passed. The user subsequently authorized flashing `eb3c2632` on
2026-11-14. Segmented writes through the device serial port verified all three
component hashes and retained NVS. The matching native server was installed with
the existing private configuration and database. Health and `whole_messages`,
`reply_sse` and `staged_recording` capability checks passed. Boot matched ELF
`f07fd38cf`, joined Wi-Fi and discovered the gateway (`<gateway-ip>:8080`). Application-ready
free heap was 83,700 bytes with a 73,728-byte largest block. A three-minute observation covered startup and entry into the conversation screen,
with no panic or unexpected reset. Reported minimum free heap was 73,740 bytes.
The serial monitor closed normally. The user then exercised recording and reply
delivery and reported slow SSE presentation and curly quotation marks rendered
as question marks. The whole-message workload memory peak remains unmeasured.

The prepared-artifact checkpoint preceded this later authorized flash.
Current streaming acceptance and a measured recording/reply workload are recorded
in the follow-up below. The previous five-minute
`e36c9dbf` observation measured 74,008–81,048 bytes free during recording, a
40,960-byte largest block, 1,340 bytes of PCM-task stack headroom and 1,884 bytes
of network-task headroom. That observation caught an HTTP 500 after successful
capture. A real chunked-HTTP regression reproduced the server EOF/deadline cleanup
fault, and its correction passed. A subsequent device recording produced a ready
draft and a completed Hermes reply in server state. These measurements precede
the whole-message cache implementation.

## Streaming latency follow-up

On 2026-11-14, a temporary, generated-text conversation requested roughly 200
Chinese characters without tools. Hermes delivered the first text after 8,078 ms;
the gateway published it at 8,079 ms and a local SSE subscriber received it at
8,082 ms. The final delta arrived at 14,076 ms and completion at 14,412 ms.
The 155 upstream deltas contained 204 characters. Reply persistence took 99 ms
in total, with a maximum of 1 ms for one append. This sample located the main
first-text delay upstream and did not show a gateway forwarding bottleneck.
It does not establish the cause of the earlier 90-second device conversation.
The temporary session was deleted locally and in Hermes after completion.

The gateway records chat-start, first-text, first-published and final timing,
reply bytes, delta count and cumulative/maximum append time. These diagnostics
contain message IDs and timings, without input, reply bodies or credentials.
The device reduces its SSE read inactivity timeout from 250 ms to 40 ms and
avoids reapplying unchanged card/transition background styles. Native LVGL
checks verify that ten stable chat frames cause no display flushes. The fonts
also add the missing curly quotation marks and related punctuation. Corrected
device streaming speed and physical glyph rendering were subsequently checked
by the user on `9b06632b`.

Corrected firmware `9b06632b` passed the complete gate and independent archive
verification. Full-image SHA-256 is
`9b06632b867ca8a704a2bff6d685b51d1ef68e2e95072dec979d203159f8a430`;
matching ELF SHA-256 is
`17c3f99b3113e2ceae998fea620f32c114f9166e73ff0aaa7c2a3aae8169dc5e`.
The bundle is `build/firmware/9b06632b867ca8a704a2bff6d685b51d1ef68e2e95072dec979d203159f8a430/`.
The app is 3,531,440 bytes, 3,984 bytes larger than `eb3c2632`; static DRAM
BSS remains 47,656 bytes. The matching prepared and running native server has
SHA-256 `da40dfbac621a593bd874dc93dff304fb67b82e56b15fc5a20a0cd2a12f91857`.
Host and native LVGL/client ASAN/UBSAN checks passed. The user authorized
`9b06632b`, and segmented flashing to the device serial port completed on
2026-11-14. All three component hashes verified. The partition table was
unchanged and NVS was excluded from writes. Startup matched ELF `17c3f99b3`,
restored the network and discovered the gateway (`<gateway-ip>:8080`). Application-ready
free heap was 83,548 bytes with a 73,728-byte largest block. The three-minute
serial observation covered one recording, successful HTTP 200 transcription
and a completed Hermes reply. Recorded minimum free heap was 70,608 bytes;
the lowest sampled largest block was 63,488 bytes. Capture/network stack
headroom was 1,360/1,816 bytes. At the end, free heap was 89,216 bytes and the
largest block was 69,632 bytes. No panic or unexpected reset was observed; the
serial port closed normally. Hermes first text took 11,794 ms, first publication
11,795 ms and upstream completion 15,284 ms. Its 90 reply deltas took 52 ms of
cumulative persistence time. Flash/startup/discovery and recording/reply
transport checks passed. The user subsequently confirmed improved SSE
appending and correct Chinese quotation marks on `9b06632b`. This acceptance
covers those two reported issues. Long-message stress, reconnection recovery
and manual scroll-follow behavior still require separate device checks.

## Completed-reply reading checkpoint

The user requested a single reveal after generation because live appending moved
the reading position too quickly. The card now keeps its animation throughout
active message states and reveals the reply at the top after completion. Cached
SSE updates stay in the background. The UI copies and lays out the reply when it
is ready for reading. Automatic-follow state and its client API were removed.
While waiting, Up can show the user input. Failed/interrupted execution reveals
retained text with its status. Host checks and native LVGL ASAN/UBSAN regressions
passed, including hidden partial text, completed replies above 10 KiB opening at
the top, stable manual reading, input/history navigation and retry/error outcomes.

Firmware `1617a2dc` passed the complete repository gate and independent archive
verification. Full-image SHA-256 is
`1617a2dca8c237d5e96636c767adc0fca2570dd2b11abfdc9703693e061d065e`.
Matching ELF SHA-256 is
`426bfe6f5997eab8a96f87c53a6655208f5396139105984a840ec686e559fb62`.
The bundle is `build/firmware/1617a2dca8c237d5e96636c767adc0fca2570dd2b11abfdc9703693e061d065e/`.
The app is 3,531,232 bytes. The partition table matches `9b06632b`, permitting
segmented writes that retain NVS. The user authorized `1617a2dc`; all three
component writes verified, and startup matched ELF `426bfe6f5`. Wi-Fi and gateway
discovery succeeded. About 119 seconds of serial observation covered two saved
recordings with HTTP 200 transcription responses and one completed Hermes reply.
Minimum recorded free heap was 69,596 bytes, with a lowest sampled largest block
of 65,536 bytes. PCM/network stack headroom reached 1,340/1,276 bytes. The last
log reported Wi-Fi suspension, followed by serial disconnection; later enumeration
found the USB port again. The monitor closed its handle on disconnection. No
panic or unexpected reset was captured in that observation window. Visible
completion/reading acceptance was not explicitly reported before the user's
subsequent compact-layout request.

## Sequential review after the compact layout

Three fresh independent agents reviewed the full project in sequence. Each round
started after the preceding review, fixes and focused validation had finished.

1. Round 1 confirmed two P2 boundary defects. The five-minute audio limit shared
   its request deadline with connection/finalization work, and a full 64-segment
   draft could not allocate a replacement recording. The request now has one
   bounded minute of grace while PCM duration and byte limits stay at five
   minutes. Capture duration follows the accepted PCM sample count. A unique
   replacement reservation supports a full draft, same-operation retries,
   cancellation, expiry and restart recovery. Ordinary appends retain their
   64-segment limit, with at most one extra temporary metadata entry.
2. Round 2 confirmed one P2 capture-generation defect with two scheduling
   windows. A delayed edit could cancel a newer capture, and late registration
   could cancel/replace the newer task. Edits now return their retired generation
   transactionally, cancellation matches that generation, and registration
   rejects older/equal generations before replacing an active task.
3. Round 3 independently reviewed the resulting full tree and found no further
   actionable defect. Firmware host tests and fresh race checks for the data,
   stream, PCM and model packages passed.

Final checks passed the complete repository gate, all Go race tests, deterministic
cancellation/registration regressions repeated 20 times, client ASAN/UBSAN, and
a native simulation of the actual capture task. That task produces exactly
9,600,000 PCM bytes at the five-minute limit. User release, queue exhaustion and
audio-read failure cases also passed ASAN/UBSAN. No real provider request or
runtime deployment was performed during these reviews.

Final firmware `28aab2eb` passed independent archive verification. Full-image
SHA-256 is `28aab2eba680d8ee9cebada2c574fd42cb756ff1a7d12891422489d1ebab4955`.
Matching ELF SHA-256 is
`6b3cf99ecd7e16850a1c30e54c40587afa2b045f017c9d195250d8d896531122`.
The bundle is `build/firmware/28aab2eba680d8ee9cebada2c574fd42cb756ff1a7d12891422489d1ebab4955/`.
The application is 3,531,472 bytes, static DRAM BSS is 47,944 bytes and
initialized DRAM is 15,252 bytes. The partition table matches accepted build
`077092ec`. The matching native server is
`build/server/sequential-review/voice-agent-server`, SHA-256
`af75c3eb6d4a731ff598e9ad54404fd0c9e4437ead755a21db4aef051cd171a4`.
The new firmware requires the server's `replacement_reservation` capability.
The user authorized the paired update on 2026-11-14. The verified server binary
was installed with the existing private configuration and database. Health,
`replacement_reservation` and the five-minute advertised audio limit passed.
Segmented flashing of `28aab2eb` verified all three components and retained NVS.
Boot matched ELF `6b3cf99ec`, restored Wi-Fi and discovered
the gateway (`<gateway-ip>:8080`). About two minutes of startup/idle observation recorded no
panic or unexpected reset. Application-ready free heap was 83,208 bytes; minimum
free heap was 73,348 bytes, and the final free heap was 82,076 bytes. The largest
sampled block remained 69,632 bytes. The user deferred functional testing, so the
monitor was intentionally stopped and its serial handle closed. Flash verification,
startup and discovery passed. Re-record replacement, send/reply acceptance,
board-level five-minute capture, long-text pressure and interrupted-network
recovery remain unverified on this build.

## Settings acceptance follow-up

The user reported that `44e6fffe` initially painted unfocused rows as surfaces,
that the light palette was too bright, and that the schedule hint included a
missing dash glyph and appeared in manual theme modes. Firmware `14f33ba8`
initializes session/settings rows with the unfocused background, uses a warm
beige light palette, and displays `07:00-19:00` only for Auto. Manual modes also
collapse the unused hint area. Static glyph checks now include non-ASCII
punctuation and decode C UTF-8 byte escapes before checking coverage.

Complete build, host, Go race/vet and smoke checks passed. Native LVGL regression
checks passed first entry, focus movement and theme changes on both row lists,
and Auto/manual hint visibility and sheet geometry. Full-image SHA-256 is
`14f33ba8f72da8581140620d0450f49be47b00f49abfa53b32773d054cccb2e6`.
Matching ELF SHA-256 is `eb41902cb3399722c24e846561d84a960105b6b7b0193184e1ff2f58f9f0c945`. Application size remains
3,520,400 bytes. The verified archive is `build/firmware/<full-image-sha256>/`.
The user subsequently authorized flashing `14f33ba8`. Segmented flashing on
2026-11-14 through the device serial port verified all three component hashes.
The 30-second boot observation matched ELF identity `eb41902cb`, connected to
the gateway (`<gateway-ip>:8080`) and showed no crash. Application-ready free heap was
80,180 bytes with a 69,632-byte largest block. The monitor closed normally.
The selected-row, beige-palette and Auto-hint appearance await user feedback.
Long-reply on-device acceptance is still pending.

The user then explicitly requested a fresh provisioning test. A full 8 MiB chip
erase followed by the verified `14f33ba8` merged image at `0x0` completed on
2026-11-14. The chip erase and write hash verification passed. The 30-second boot
observation matched ELF `eb41902cb`, loaded defaults (volume 80%, screen-off 30 s,
sleep 5 min, UTC+08:00), and reported no stored Wi-Fi credentials. Application-ready
free heap was 83,856 bytes with a 73,728-byte largest block before station
connection. The serial monitor closed normally. The device is ready for the user
to enter Settings → Network and start Bluetooth provisioning. This fresh-start
check supersedes the earlier retained-settings state.

## Modern UI validation checkpoint

Firmware `44e6fffe` implements fixed message cards, both approved palettes,
Light/Dark/Auto settings, local time and editable UTC offset, plain session titles
and removal of prototype data migration helpers. It passed the complete build,
host, Go race/vet and binary smoke gate. Native LVGL tests exercised seven-line
Chinese card boundaries, local/remote paging, changing reply state, draft loading,
in-place theme preview/cancel and the actual theme/time-zone settings editors.

Full-image SHA-256 is
`44e6fffe5e30e8f3134a7f5edd37c052f020f04dfb799ab2fc56754e0c8b9df3`.
Matching ELF SHA-256 is
`f3c6c0b040383a751ff7e96397567fc5f1a916159a6adcd8a9b56bc7d5e6a159`.
The verified archive is `build/firmware/<full-image-sha256>/`. Application size is
3,520,400 bytes. Static BSS is 50,808 bytes, 24 bytes above the installed
`54c86d78`. Initialized DRAM remains 15,252 bytes. Both themes share one widget
tree. Capture-load RAM still requires device measurement.

The matching native server at `build/server/modern/voice-agent-server` is running
with the existing private configuration. Health, `message_cards: "passport-v1"`,
staged recording and the empty session index passed live local checks. Linux
AMD64/ARM64 builds passed. The user authorized and completed segmented flashing
of `44e6fffe` on 2026-11-14 through the device serial port. All three component
hashes verified. The 30-second boot observation matched ELF identity `f3c6c0b04`,
connected to the gateway (`<gateway-ip>:8080`) and showed no crash. Application-ready free heap
was 80,144 bytes with a 69,632-byte largest block. The serial port closed after
observation. Local clock sync, theme/time-zone editing and long-card navigation
await user acceptance.

## Earlier startup status and card-page checkpoint

The empty session page uses the list's flat background, 16 px typography and
primary/secondary green colors. A centered 40 px, one-pixel line icon accompanies
the title and explanation. The borderless group shares the list's horizontal
insets. Explanations occupy at most two lines with ellipsis for longer errors.
States cover missing network configuration, connecting a saved network, gateway
discovery, the first session-list read, an empty ready list and connection errors.
Status widgets and list rows are created on demand and released when the visible
content changes.

Conversation detail shows one fixed 216×244 px card at x=12, y=52. Its role,
page counter and status have fixed positions. The body uses seven lines with a
23 px line pitch. Up/Down turns pages, then crosses to the adjoining message.
The gateway's `passport-v1` profile uses conservative bounds from the actual font
to insert display line breaks and calculate per-message page totals. The source
text and byte offsets remain unchanged. The device caches at most four cards.
Loading an adjoining cache retains the displayed card. Stream updates extend the
visible page and update its total without jumping to the end. The initial view
opens the latest assistant reply at page one. Earlier user input is reached with
Up. Reviewed drafts use the same page layout and send their persisted revision.

Firmware `844495a5` implements this follow-up to the long-message layout report on
`54c86d78`. Its complete build, host, race/vet and smoke gate passed. Native LVGL
checks passed full seven-line Chinese bodies, forward/backward cache transitions,
fixed roles, changing status and draft page loading. Full-image SHA-256 is
`844495a590a31f73fad00805da1feaf71ef554eb882a07da447b19c224277284` and matching ELF SHA-256
is `e56e48530dd422c34df6fd81e6700bea66f8c295dc2f3e409e87bfb16faea4d6`.
The verified archive is `build/firmware/<full-image-sha256>/`. Application size is
3,518,480 bytes. Static BSS is 50,760 bytes, 24 bytes below `54c86d78`.
This card checkpoint has not been flashed. The user subsequently approved both
modern palettes and requested manual/automatic switching plus local time and
time-zone settings. The implementation and latest build are recorded below.

## Pre-commit review validation

Three parallel review tracks covered firmware, server persistence/protocols and
integration/security. They confirmed five defects, all now fixed. The user then
requested three sequential full-project reviews. Those rounds are tracked below
as each finishes, with fixes and validation between rounds.

| Priority | Defect | Correction |
| --- | --- | --- |
| P1 | Exiting re-recording could still replace the old draft | Persist staged results and automatically confirm saving after the cancellable phase |
| P2 | Late text reads could swallow a previous-window request | Validate request-generation identity on successful and failed reads |
| P2 | Restart recovery mixed unfinished replacement text into the prior draft | Persist recording mode and retire unaccepted replacement text during recovery |
| P2 | A near-limit draft rejected a short re-recording | Bound temporary replacement text independently |
| P2 | An incompatible first discovery candidate hid a compatible gateway | Rotate by endpoint identity across usable candidates and address fallbacks |

The sequential reviews have the following checkpoints. Each round uses a fresh
independent reviewer covering the full project. Fixes are frozen and validated
before the next round starts.

| Round | Confirmed defects and corrections | Validation |
| --- | --- | --- |
| 1 | Serialize session lifecycle operations and re-read persisted state, reject message retries during deletion, include draft separators in the text budget. A supplementary startup check also moved listener acquisition before database recovery. | Complete static, race and binary smoke gate passed after all four fixes |
| 2 | Preserve recording text identity across late conversation reads, accept the retry endpoint's HTTP 200 message contract, handle JSON escapes during bounded media filtering | Targeted regressions and complete build, host, Go race/vet and binary smoke gate passed |
| 3 | No further actionable defects confirmed | Independent full-project read-only review, firmware host checks, Go race/vet and diff checks passed |

Client tests include the complete production translation unit. They cover
cancellation during recording/finalization, the saving boundary, lost commit
responses, text-window ABA races and stale errors. Native LVGL tests execute
actual screen lifecycle and render functions to verify that exiting keeps the
list visible across delayed completion. SQLite/HTTP tests cover fresh schemas and unsupported-version rejection,
commit/abort competition, duplicate retries, abandoned-stage expiry and receipt
preservation across repeated append capture. Staged capture IDs are single-use.

Firmware `54c86d78` passed the complete build, host, Go race/vet and binary smoke
gate after round 2. Round 3 required no further source changes. Full-image SHA-256
is `54c86d780f56a93cc40d93be05aa721b1510947a3c0d890b32fa1241837e4ce0`.
Matching ELF SHA-256 is `14a90ad2f9424cb9a6618b6011adc4970b73adbde690eccffbc0ff623f6b3824`.
The verified archive is `build/firmware/<full-image-sha256>/`. Application size is
3,518,944 bytes. Static BSS is 50,784 bytes, unchanged from `0838950e` and 24 bytes
above `04c3dd73`. Linux AMD64/ARM64 and the local native server builds passed.
The native candidate is `build/server/review/voice-agent-server`.

This firmware requires the matching gateway's `staged_recording` capability.
On 2026-11-14 the authorized local gateway update completed, SQLite migration to
v2 was confirmed, health and required capabilities passed, and mDNS advertised
the gateway (`<gateway-ip>:8080`). Device acceptance remains recorded for `04c3dd73`.
The authorized segmented flash of `54c86d78` completed on 2026-11-14 through
the device serial port. All three component hashes verified. The 30-second boot
observation matched ELF identity `14a90ad2f`, connected to the gateway and showed
no crash. Application-ready free heap was 80,464 bytes and the largest block was
69,632 bytes. The serial port closed after observation. User acceptance of
cancel/re-record/send behavior is pending. Capture-load memory and live fault
recovery remain unverified.

## Earlier accepted build 04c3dd73

Firmware `04c3dd73` passed the complete build, host, Go race/vet and binary smoke
gate. The independently verified full-image SHA-256 is `04c3dd734f19640f0235bb246273297e4546390552cc41d20837061adde8a720`.
Matching ELF SHA-256 is `9d486581e036e1f5a62c917fb8db1b7dede4c0709d89edb3529bc9017f3b82bc`.
The bundle is `build/firmware/<full-image-sha256>/`. Application size is
3,517,520 bytes.

Native LVGL tests used the actual chat build, render and button-scroll functions.
They reproduced the preceding negative y=-41 overscroll, then verified clamping
at both ends, repeated key steps, a 23 px body line pitch, increased role/body
separation, active-status row resizing, preservation of a shared paging anchor
and bounded draft scrolling.

On 2026-11-13 the authorized segmented flash verified all three components.
The 30-second startup observation confirmed ELF identity `9d486581e`, reached
the gateway (`<gateway-ip>:8080`), and found no crash. Application-ready free heap was
80,480 bytes, with a 69,632-byte largest block. The serial monitor closed normally.
The user confirmed that the top blank space, body line spacing and role/body
separation were all satisfactory. This completes the requested spacing acceptance.
Capture-load memory and broader fault recovery remain separate project checks.

## Font validation and device handoff

Firmware `5805f22d` passed the complete build, host, Go race/vet and binary smoke
gate. Font reproduction matched all three maintained payloads. Native LVGL checks
passed all 6,763 GB2312 Chinese glyph descriptors, U+6E25 rendering, the 20 px
fallback and the proportional `100%` field using 40-line partial rendering.
Full-image SHA-256 is `5805f22d79416ca5074cba39ccb677713771ec21516c492d130cae8b9d5469cb`.
Matching ELF SHA-256 is `c840c0e6d6090bb89d7e347bf710c614b6960b5b525c25d2ab91697488e40db4`.
The verified bundle is `build/firmware/<full-image-sha256>/`.

Compared with `316a77a4`, the application image grew by 409,488 bytes (about
400 KiB) to 3,517,488 bytes. Initialized static RAM grew by 72 bytes to 15,252
bytes. The zero-initialized static section is 50,760 bytes. Runtime heap and stack
margins require capture-load measurements. On 2026-11-13 the approved segmented
flash passed all three component checks. A 30-second startup observation found
no crash, confirmed ELF identity `c840c0e6d`, and reached the gateway at
the gateway (`<gateway-ip>:8080`). The application-ready log reported 80,368 free heap bytes
and a 69,632-byte largest block. Serial observation ended and closed the port.
The user confirmed Chinese glyphs and consistent prompt/list styling. The user
requested tighter body lines, more separation below role labels and removal of
the remaining blank space above messages. The subsequent bounded-scroll and
spacing revision passed user acceptance on `04c3dd73`, recorded above. Capture
memory margins require workload measurements.

## Pending compact-layout validation

Firmware `316a77a4` passed the complete firmware, host, Go race/vet and binary
smoke gate. Native LVGL rendering checked all six status states, the list and
compact chat cards. Role glyph measurements confirmed the layout's y=7..23
bounds. The archive was independently verified after the build.
Full-image SHA-256 is `316a77a49858c961c193f7ed336e2e8f46affc28e2ccfba53e7a0e00eb900a35`.
Matching ELF SHA-256 is `036cd6a1d71c7bbf24b76cf9e899e16f270c074e3f1203c7037fe4ce0979559d`.
The bundle is `build/firmware/<full-image-sha256>/`. Device tests have not run
for this build. Status/list consistency, normal and active-status chat spacing,
long-message navigation and recording/send/reply behavior await acceptance.
The user postponed flashing this build on 2026-11-13.

## Status-card device feedback

Firmware `fd858e3d` passed the complete build, host and server-compatibility gate.
The verified full-image SHA-256 is `fd858e3df877ec84178f2e64d17d34d0b57692bf479ae1692f9c367636225032`.
The matching ELF SHA-256 is `677d1e829aec67d2fb60685f640314bdca40683dc1714a7cf8888abd52472c56`.
The bundle is `build/firmware/<full-image-sha256>/`. Segmented flash, boot and
discovery passed. The user confirmed instant scrolling with no ghosting, and
requested closer visual alignment with the list and less space above chat content.
The layout revision described above awaits a new device test. The accepted build
below records the preceding interaction.

## Accepted navigation build 9cdc158a

Firmware `9cdc158a` passed the complete build and host/server gate. The local
conversation endpoint returned both roles with a four-block limit. Its loopback
median was 0.92 ms over 12 reads. Segmented flash verification, boot and gateway
discovery passed. The application reported 78,272 free heap bytes and a
65,536-byte largest block. On 2026-11-13 the user confirmed top-level page cycling,
editor exit, sleep/wake, both conversation roles, visibly improved paging and
successful recording/send/reply display. The transient recording hint was absent
during tested navigation. View snapshots occupy 13,896 static bytes, compared
with 10,320 bytes in the previous build. Capture-load heap and stack margins,
long recordings, fault recovery and sleep current require targeted measurements.

Full-image SHA-256 is `9cdc158ade7bc8b0004b747a6fa1854b24f6542cc1f20efdaa0ed041e8f37179`.
Matching ELF SHA-256 is `cb35aab1d6f24da26baea2b17841f80362aaf80a718ef6021485119cb7fd539d`.
The verified bundle is `build/firmware/<full-image-sha256>/`.
The accepted build below records the earlier interaction.

## Previous accepted build 06d4d2d5

Firmware `06d4d2d5` passed the complete host/server/build gate, segmented flash
verification, boot and gateway discovery. The user confirmed Long Down exits the
brightness editor, settings and conversation at their expected levels, successful
re-recording shows only the replacement text, and Short OK sends it directly and
displays the Hermes reply. Live integration used the user's on-device test audio.
The gateway recorded the completed reply with an empty pending draft.
The application-ready log reported 82,980 free heap bytes and a 73,728-byte largest
free block. Capture-load memory and stack margins, long recordings, connection
faults and sleep current still require targeted measurements. The verified full-image SHA-256 is `06d4d2d5612217bbd68584e7c5582a2ef21b13e83cff7b16b2390fee034494cc`.
The matching ELF SHA-256 is `d64f49b92265ffc5e7065b8a85b6405e5d56a26b8b23cb71997721efdb1430ab`.
The bundle is under `build/firmware/<full-image-sha256>/`.
The historical device results below refer to `697a36c6`.

## Device validation on 2026-11-13

Firmware `697a36c6` passed segmented flash verification, boot and LAN gateway
discovery. The user confirmed recording, appending a segment, transcription,
explicit sending and Hermes reply display. The gateway recorded a completed
message. Long Up toggles sessions/settings in both directions. Cancelling an
unsaved brightness edit by Long Up restored the saved brightness in device tests.
Further interaction refinement is in progress.

The initial application-ready log reported 83,096 bytes of free heap and a
73,728-byte largest free block. Capture/Wi-Fi workload memory and stack margins,
recording latency, microphone quality, BLUFI transitions, long recordings,
fault recovery and deep-sleep current still require targeted measurements.

The verified full-image SHA-256 is
`697a36c6d896f074f5d8fbc8c85a9da89e58c4a92af6b4ccae2c9d28e11c4fc2`.
The matching ELF SHA-256 is
`a1ef9b57a9279c905a71ac0c62f18dcfd9affe70a05498e02bccda6d80c6208d`.
The bundle is under `build/firmware/<full-image-sha256>/`.
