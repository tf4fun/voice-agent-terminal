<p align="right">
  <strong>简体中文</strong> · <a href="server.md">English</a>
</p>

# Go 服务端

## 配置

将 [server/.env.example](../server/.env.example) 复制为 `server/.env.local`，
从 `server` 目录启动程序，或使用 `-env /absolute/path/to/file` 指定文件。
文件支持字面值 `NAME=value` 和匹配的外层引号，进程环境变量优先。

| 变量 | 含义 |
| --- | --- |
| `VOICE_ADDR` | 监听地址，默认 `:8080`，命令行 `-addr` 可覆盖 |
| `VOICE_DATA_DIR` | Go 服务 SQLite 目录，默认 `./data` |
| `VOICE_MDNS` | 在活动局域网广播，默认 `true` |
| `VOICE_MDNS_NAME`、`VOICE_MDNS_INTERFACE` | 显示名称与可选局域网接口 |
| `VOICE_API_KEY` | 可选的设备请求 Bearer 凭据，健康检查仍可读取 |
| `VOICE_CHAT_WORKERS` | 并行会话处理数，默认 2，范围 1–8 |
| `HERMES_BASE_URL` | Hermes API 根地址，默认 `http://127.0.0.1:8642`，可包含 Profile 前缀 |
| `HERMES_API_KEY` | Hermes API 服务凭据 |
| `HERMES_MODEL` | 请求模型或路由别名，默认 `hermes-agent` |
| `HERMES_SYSTEM_PROMPT` | 每次对话请求最前面的 system 消息，Hermes 将其叠加在自身提示之上。每条消息保存发送时的提示，修改只影响新消息，重试沿用原提示。默认要求回答控制在 300 字以内，用户明确要求长文时除外 |
| `VOICE_ASR_PROVIDER` | 默认 `openai`，离线转写测试使用 `fake` |
| `OPENAI_API_KEY` | Go 持有的 OpenAI 凭据 |
| `OPENAI_TRANSCRIBE_MODEL` | 默认 `gpt-live-transcribe` |
| `OPENAI_REALTIME_URL` | 默认 `wss://api.openai.com/v1/realtime?intent=transcription` |
| `VOICE_TTS_PROVIDER` | 回复朗读：默认 `openai`（使用 `OPENAI_API_KEY`），也可为 `fake` 或 `off` |
| `OPENAI_TTS_MODEL`、`OPENAI_TTS_VOICE`、`OPENAI_TTS_URL` | 默认 `gpt-realtime-2.1-mini`、`marin` 和 `wss://api.openai.com/v1/realtime`，见[语音回复](#语音回复) |

本地数据与环境文件由 Git 忽略。网络暴露范围和设备凭据按预期的局域网部署配置。
Hermes API 服务应已启用，并提供对应的 `HERMES_API_KEY` 与 API 会话能力。

## 网页聊天

打开服务根地址，例如 `http://localhost:8080/`，即可使用与设备共享会话和回复的
聊天页面。布局参照常见的聊天网页：侧栏按最近活动分组列出会话，支持标题搜索、
重命名和删除；主区为对话与输入框。配色沿用设备的浅色与深色调色板角色，主题可
跟随系统或在侧栏切换。窄屏下侧栏改为滑出式。

- 回复通过 `GET /api/v1/events` 实时流式显示。事件流用 `fetch` 读取，以便携带
  Bearer 密钥。增量按 UTF-8 字节偏移拼接；出现缺口或 `chat.reset` 时重新读取
  完整回复，重连后核对最新一页消息。回复以 DOM 节点渲染安全的 Markdown 子集
  （标题、列表、引用、表格、可复制的代码块、链接），从不拼接 HTML 字符串。
- 输入框文字保留在本页（按会话保存，直到发送或刷新）；设备的语音草稿只留在设备上。
  发送时提交完整文字。新对话在第一次发送前没有会话，第一次发送才创建会话并以首行
  命名；响应丢失时用相同 ID 重试。
- 设备发出的消息在首个事件到达时即显示，页面跟随到底部并持续跟随流式回复。
- 最新一轮回复失败或未确认时可重试。消息和代码块可复制，纯 HTTP 局域网页面同样可用。
- `#/c/<会话 ID>` 链接可直接打开会话。Ctrl/Cmd+K 新建对话；Enter 发送，Shift+Enter 换行。

配置了 `VOICE_API_KEY` 时，在页面输入该中转服务的访问密钥。密钥默认仅保留在页面
内存中；勾选“在此浏览器中记住”后存入 `localStorage`，退出连接时清除。公开的
HTML、CSS、JavaScript 与图标页面壳不含会话数据或凭据；接口请求保留 Bearer 认证
及同源修改保护，页面的内容安全策略只允许同源资源。页面资源通过 Go 标准库的
`embed` 编入二进制。

删除复用现有会话接口，同步删除 Hermes 对应会话。等待同步时继续显示删除中状态。
存在活动或未确认消息时，沿用现有忙碌响应。页面列出此中转服务管理的会话。

## 发现与限制

Go 广播 `_voiceagent._tcp.local`，携带 `ver=2`（即 `/api/v1/server` 报告的协议版本）、
`path=/api/v1`、`caps=chat,pcm16` 和 IPv4 备用地址。仅监听回环地址时跳过 mDNS。
广播失败会记录日志，HTTP 服务继续可用。设备发现后读取 `/api/v1/server`，要求协议 2。

- 会话、转写和消息标识采用小写 UUID 格式。
- 元数据每页默认 4 条，支持 `limit=1..20` 和偏移。`has_more` 表示是否还有下一页。
- 消息输入上限 64 KiB，单次转写上限 16 KiB。
- 实时转写及可选窗口读取使用 UTF-8 字节偏移，每个窗口最多 2048 字节。
  完整消息正文接口返回整条已保存内容。
- 每个 SSE 帧最多 1024 字节，增量文字最多 64 字节。
- 每条过滤后的回复最多 2 MiB，上游 SSE 过滤后事件上限为 256 KiB。
  普通文字事件过大时，明确转入结果未确认状态。
- 最多同时接收四条音频和 64 个 SSE 订阅。音频时长上限为五分钟，完整上传／转写
  请求有六分钟预算，用于连接准备、传输收尾和最终识别。聊天超时为十分钟。

## 会话

会话由它的第一条消息创建（见[明确发送](#明确发送与回复)），没有单独的创建请求，
因此不存在没有消息的会话。联系 Hermes 前先持久化创建记录，标题取消息第一个非空行
（最多 24 字，超出加 `…`）。Hermes 创建失败时会话保持 `creating`，消息保持排队；
后台恢复复用同一个上游 ID，会话就绪后再发送消息。Hermes 返回 409 时核对已有
API 会话。如果 Hermes 返回 `400 invalid_title`，则保持同一会话 ID，省略标题重试
一次，本地索引保留原标题。改名会核对上游归属、更新 Hermes，再更新本地标题。上游
失败时保留本地原名，供用户明确重试。该索引覆盖通过 Go 创建的会话。

`GET /api/v1/sessions` 返回本地会话索引。对 `/api/v1/sessions/{id}`
使用 `GET` 读取元数据，使用 `PATCH` 修改 Hermes 与本地标题。`DELETE` 将状态改为
删除中，协调上游删除后保留已退役的本地 ID。活动或结果未确认的消息会返回忙碌；
仍处于 `creating` 的会话除外：它排队的首条消息从未发出，因此总能删除，消息随会话
一并删除。之后才成功的上游创建也会被删除。
删除前核对上游会话的 API 来源。

## 转写

自协议 2（2026-11-16）起，转写是旁路：把一次录音转成文字交给客户端自己的草稿，
不在 SQLite 中保存任何内容。草稿发送前不会到达中转服务。

网关常驻一个已配置好的 OpenAI 实时转写会话。OpenAI 实时转写指南在同一会话中发送多段音频：
每段结束时提交，并按 `item_id` 匹配转写事件，因为不同段的完成顺序不保证。每次录音先发送
`input_audio_buffer.clear`，追加音频后只提交一次，并只接受清空后的第一个条目。正常结束的
会话放回池中；失败或取消的会话直接丢弃，因为其中可能残留音频或进行中的条目。每个连接由
专用读协程处理每 20 秒一次的保活 ping，断线立即发现并以最长 60 秒的指数退避重连。会话
在 50 分钟时轮换，低于文档所列的实时会话时长上限。同时进行的第二段录音会另开会话。

`POST /api/v1/transcriptions` 携带 `{"id":"<uuid>"}` 时，网关在返回 `201` 前预留常驻会话（没有则新建），
最多等待 3 秒，低于设备 4 秒的请求超时。设备在收到响应前保持“准备录音”动画，
因此录音开始时转写会话已经就绪。预留会话归属该转写 ID，只交给一次上传，闲置 30 秒
或网关停止时放回池中。日志记录准备耗时和转写失败代码，不含凭据。

握手超过 3 秒或上传没有预备会话时，音频上传同样从第一个字节起即被读取，与 OpenAI
实时会话建立同时进行。经代理冷连接握手约需
2.5 秒；此前网关在收到 `session.updated` 前不读取请求体，TCP 背压使设备 8 KiB PCM
队列在 2.8–2.9 秒溢出。预读缓冲每次录音最多 16 MiB，超过 300 秒 16 kHz 完整录音，
按实际到达的数据分块分配；请求本身的字节上限仍然生效。

`POST /api/v1/transcriptions/{id}/audio?sample_rate=16000` 接收原始单声道、
小端 PCM16，使用 `Content-Type: audio/pcm`。输入采样率支持 16000 和 24000，允许
分块请求，EOF 表示提交本段音频。Go 增量地将 16 kHz 输入重采样为 24 kHz，并连接
OpenAI。成功时返回 `200`，整个 `text/plain; charset=utf-8` 响应体就是去掉首尾空白的
最终转写。转写为空返回 `422 no_speech`，ID 重复使用返回 `409`，四路并发上传已满返回
`409 busy`，上传过大返回 `413`，上游失败返回 `502 transcription_failed`。断开连接即
取消转写。音频字节只按流处理。

`GET /api/v1/transcriptions/{id}/text` 通过有界 UTF-8 窗口接口（`offset`、`limit`、
`before=<字节边界>` 或 `tail=1`；修订号计更新次数）读取进行中转写的实时文字。文字在
上传期间及结束后一分钟内保存在内存中，同时最多保留 16 条（满时淘汰最早结束的一条）；
未知或已释放的 ID 返回 404，上传开始前同样返回 404。

`/api/v1/server` 报告 `protocol: 2`，以及 `bounded_paging`、`text_before`、
`text_tail`、`whole_messages`、`reply_sse`、
`max_input_bytes`、`max_transcript_bytes`、`device_logs` 和 `speech`；设备要求
协议 2。SQLite 结构 v5 只保存会话、消息（含各自发送时的 system 提示）和设备诊断报告。启动时拒绝更早的数据库
而不做迁移：删除 `gateway.db` 后从空库开始。

## 完整消息

设备和管理页通过 `GET /api/v1/sessions/{id}/turn` 选择完整的一次用户提问／助手回复。
响应包含 `message` 元数据以及 `before`／`after` 消息 ID。尚未发送消息的会话返回
`{"message":null,"before":"","after":""}`。`index`（从 1 开始）和 `total` 给出该轮在会话中的位置与总轮数。历史读取通过 `message=<uuid>`、
`before=<uuid>` 或 `after=<uuid>` 选择其中一个参数。相邻消息查询限定在当前会话内，
到达历史边界时返回 404。无效或重复的选择参数返回 400。回复生成期间设备每秒轮询
本轮，因此该查询只读元数据（大小、状态和修订号），不读取正文。

通过 `GET /api/v1/sessions/{id}/messages/{message}/content?part=input|reply`
读取完整 UTF-8 正文。纯文本响应包含精确的字节长度 `Content-Length`、`X-Revision`、
`X-Message-State` 和 `Cache-Control: no-store`。正文保留原始换行及排版。完整消息
响应使用消息级标识。

可选 `revision=<n>` 要求精确版本，版本推进后返回 409。流式恢复时先订阅 SSE，再读取
当前完整回复，以返回正文的字节长度建立增量基线。响应头中的版本及状态对应同一次
持久化快照。设备在 RAM 中保留当前输入和回复，访问历史时加载另一轮完整消息。
能力响应声明 `whole_messages` 与 `reply_sse`。

## 明确发送与回复

`POST /api/v1/sessions/{id}/messages` 接受
`{"id":"<message-uuid>","text":"<完整输入>","new_session":<bool>}`。
文字即客户端审阅过的草稿：不能为空白（否则 `400 empty_input`），最多 64 KiB（否则
`413 input_too_large`）。同一事务把它保存为固定的排队消息。带 `new_session` 时，
未知的会话 ID 在同一事务中创建，并以这段文字命名；不带时未知会话返回 404，删除中的
会话同样返回 404。响应为 202，包含消息元数据和 `session` 对象（ID、标题、状态），
客户端据此得知新会话的标题。相同消息 ID、相同会话和文字的重复请求返回同一条消息，
因此响应丢失后可以安全重试；相同 ID 但文字不同返回 409。会话中已有活动或结果未确认
的消息时返回忙碌。

后台任务通过会话 ID 和稳定的幂等键，向 Hermes Chat Completions 提交一条
新的用户消息。保存模型、system 提示、输入和上游身份摘要，保持消息关联，重试时发送同一请求。
存储层的活动消息约束确保同一会话的发送串行进行。

`GET /api/v1/sessions/{id}/messages` 返回消息元数据列表。
`GET /messages/{message}` 读取状态，`GET /messages/{message}/text`
读取回复窗口，使用 `part=input` 可读取提交的快照。

| 状态 | 含义 |
| --- | --- |
| `queued`、`sending`、`waiting` | 已持久化、已领取或等待上游输出 |
| `receiving` | 正在持久化回复文字 |
| `attention` | 上游报告待审批操作 |
| `completed` | 流以 `finish_reason: stop` 和 `[DONE]` 结束 |
| `incomplete`、`cancelled`、`failed` | 明确的上游结果或回复缓存达到上限 |
| `unconfirmed` | 断线、重启、无效回复流或上游身份变化 |

`POST /messages/{message}/retry` 在失败或结果未确认后重试同一逻辑消息，
要求后续消息尚未推进会话。恢复采用相同的 Chat Completions 请求身份，
设置 `stream: false` 获取完整回复，再原子替换此前的部分回复。
上游地址或凭据变化时，不会自动向新身份提交，消息标记为 `unconfirmed`，错误码为
`hermes_identity_changed`。用户明确重试即确认重新提交，消息随之改用当前身份范围。
此恢复面向 Hermes 的 canonical 持久接收模式，真实使用前需要验证部署模式
的幂等语义。Legacy 模式的流式重试保证有所差异。

## 事件流

`GET /api/v1/events?session_id=<uuid>` 订阅一个会话，省略筛选参数时订阅
全局事件。网页使用此接口；设备改为轮询本轮元数据。连接首先发送 `resync`，客户端读取当前整条消息元数据及完整正文建立基线。
事件包括 `session.updated`、`chat.delta`、`chat.reset`
和 `message.updated`。聊天增量携带 UTF-8 字节偏移，零偏移可能由 JSON 编码省略。
快照已包含的增量按偏移去重；偏移不连续或收到 `chat.reset` 时重新读取完整回复。
`message.updated` 将状态与持久化完整快照对齐。每十秒发送保活。

慢订阅者断开后，通过重连核对状态。事件丢失后的恢复依据是持久化元数据与完整消息快照，
SSE 投递本身是临时的。审批以待处理状态呈现，详细审批控制留给后续产品决策。

## 语音回复

`GET /api/v1/sessions/{id}/messages/{message}/speech` 流式输出回复的朗读音频。
中转服务等 Hermes 写完整条回复后，逐行去掉 Markdown 标记、代码块和链接地址，再把整段
回复作为一次请求合成，句子之间不再有每句开头的等待。响应是分帧二进制：一个类型字节、
一个小端 uint16 长度，然后是载荷。类型 1 表示朗读开始（uint16 序号 0 加正文前 240 字节；
设备显示它自己取回的回复全文），类型 2 是 16 kHz 单声道 PCM16LE 音频（每帧 128 ms），
类型 3 以一个状态字节结束（0 完成、1 失败、2 取消、3 超出大小上限）。类型 4 是空的保活帧，
没有其他帧可发时每 5 秒发送一次，使设备的读取超时能撑过较长的思考。

音频攒够 1 秒后才开始发出，之后合成快于播放，始终保持领先。整条流缓存在中转服务中：
合成在某条回复第一次被请求时开始并由所有听众共享，后来者从头重放；`?offset=<字节数>`
可在缓存流的任意字节处续接断开的连接，帧中间也可以（保活帧不进缓存，也不计入偏移；
续接的连接到达其偏移之前不发保活帧，因为设备可能正处在某帧中间）。续接只能接上它开始时
那条缓存流：这条流已经不在时（合成失败、被淘汰、网关重启，或同一消息 ID 的重试写出了
新回复），带偏移的请求返回 409 `speech_restarted`，也不会开始合成，因为重新合成的音频
不一定与已播放的字节一致。
未完成的流在最后一个听众离开 10 秒后停止合成，之后再请求会重新合成。完成的流保留
10 分钟、最多 8 路，单条回复的音频上限 16 MiB、文字上限 8000 字节。`/api/v1/server`
通过 `speech` 报告是否可用。

`VOICE_TTS_PROVIDER` 可为 `openai`（默认）、`fake`（开发用的柔和提示音，每字 100 ms）
或 `off`。OpenAI 已弃用 Speech 接口的模型（`gpt-4o-mini-tts`、`tts-1`、`tts-1-hd`，
2027-01-06 停止服务），因此中转服务把 Realtime API 当作朗读器使用：`OPENAI_TTS_MODEL`
默认 `gpt-realtime-2.1-mini`，`OPENAI_TTS_VOICE` 默认 `marin`，地址为 `OPENAI_TTS_URL`
（`wss://api.openai.com/v1/realtime`）。每条回复是一个不进入对话的 `response.create`
（`conversation: "none"`，空输入），指令要求逐字朗读；24 kHz 输出经加窗 sinc 低通
降采样为 16 kHz。上游 20 秒没有事件即判定失败，单次请求最长 10 分钟。与转写一样保持一个常驻会话。2026-11-15 在部署主机上实测，中文、英文
和中英混合句子均逐字朗读，首段音频 0.64–0.87 秒；经代理建立会话需 3.7 秒。

## 设备诊断报告

`POST /api/v1/device/logs` 接收终端上报的一份 JSON 报告（不超过 64 KiB，使用设备
Bearer 密钥）。内容包括固件版本与构建号、触发原因、启动次数、复位原因、运行时长、
内存数据、最多 1024 条事件 `[boot, ms, type, a, b, tag]` 及最多 16 KiB 警告／错误日志。
格式不合法时返回 400。原始报告存入 SQLite（保留最新 100 份），并记录一行包含报告
ID 与摘要的警告日志。`GET /api/v1/device/logs` 列出报告元数据，
`GET /api/v1/device/logs/{id}` 渲染为中文时间线，加 `format=json` 返回原始报告。
上报时所在启动周期内的事件按中转服务收到报告的时刻换算为绝对时间。网页侧栏可打开
同一列表。报告只含事件代码与日志行，不含音频、转写文字或凭据。
`internal/diag/testdata/report.json` 由固件编码器生成，用于固定传输格式。

## 测试与构建产物

`go test -race ./...` 覆盖第一次发送创建会话、发送幂等与并发、旁路转写、重启恢复、
UTF-8 窗口、流式 PCM、模拟 OpenAI WebSocket、Hermes HTTP/SSE 和媒体过滤。
`tools/smoke-server.sh` 用真实二进制与本地模拟上游，验证转写、第一次发送和重启持久化。
`node tools/test-web.mjs` 在 Node 中用简化 DOM 和预设的网关响应运行管理页 `app.js`，
覆盖发送期间输入的草稿和重连后的回复恢复。

`tools/build-server.sh amd64` 与 `arm64` 在 `build/server/linux-<arch>/`
生成 CGO-free Linux 二进制及 SHA256SUMS。GitHub Actions 执行检查并上传
两种架构产物，推送 `v*` 标签时还会发布为 Release 附件。真实上游凭据、局域网 mDNS、麦克风识别准确性与延迟、设备 RAM
仍需集成验收。

## 2026-11-15 网页聊天验证

Go 测试通过，包括 `/ui/icon.svg` 公开资源检查。在无头 Chromium 中驱动使用假转写
和假流式 Hermes 的本地中转服务，覆盖：密钥拒绝与登录、首次发送创建并命名会话、
带 Markdown 表格／嵌套列表／代码块的流式回复、第二条消息、设备追加的草稿出现在
输入框、设备暂存录音显示实时转写及提交后的文字、Ctrl+K、重命名、通过 `#/c/` 链接
刷新、记住密钥、深色主题、删除，以及 390 像素宽度下无横向滚动。部署后的中转服务
仍需对接真实 Hermes 验证。

## 2026-11-13 管理页验证

通过本机、Linux AMD64 与 Linux ARM64 服务端构建。静态检查通过宿主测试、
Go 竞态测试及真实二进制烟雾流程。管理页测试覆盖静态资源、认证后的会话读取、
同源删除，以及通过本机模拟上游验证 Hermes 同步。浏览器检查覆盖登录、分页、
提问／回复预览、删除确认取消、末页回退、空列表、退出连接及 390 像素窄屏布局。
同时在浏览器验证了正在运行的本机服务根页面。

## Docker Hermes 凭据

Hermes 可能从自身数据目录的 `.env` 读取 `API_SERVER_KEY`，覆盖容器环境。
`HERMES_API_KEY` 应填写运行中 API 服务实际使用的值。`docker compose exec`
显示的环境值与 Hermes 加载的配置文件可能存在差异。

本机开发若使用 `server/.env`，应明确执行
`go run ./cmd/voice-agent-server -env .env`。默认读取 `.env.local`。
