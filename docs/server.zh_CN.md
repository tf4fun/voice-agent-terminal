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
| `VOICE_ASR_PROVIDER` | 默认 `openai`，离线转写测试使用 `fake` |
| `OPENAI_API_KEY` | Go 持有的 OpenAI 凭据 |
| `OPENAI_TRANSCRIBE_MODEL` | 默认 `gpt-live-transcribe` |
| `OPENAI_REALTIME_URL` | 默认 `wss://api.openai.com/v1/realtime?intent=transcription` |

本地数据与环境文件由 Git 忽略。网络暴露范围和设备凭据按预期的局域网部署配置。
Hermes API 服务应已启用，并提供对应的 `HERMES_API_KEY` 与 API 会话能力。

## 网页聊天

打开服务根地址，例如 `http://localhost:8080/`，即可使用与设备共享会话、草稿和回复的
聊天页面。布局参照常见的聊天网页：侧栏按最近活动分组列出会话，支持标题搜索、
重命名和删除；主区为对话与输入框。配色沿用设备的浅色与深色调色板角色，主题可
跟随系统或在侧栏切换。窄屏下侧栏改为滑出式。

- 回复通过 `GET /api/v1/events` 实时流式显示。事件流用 `fetch` 读取，以便携带
  Bearer 密钥。增量按 UTF-8 字节偏移拼接；出现缺口或 `chat.reset` 时重新读取
  完整回复，重连后核对最新一页消息。回复以 DOM 节点渲染安全的 Markdown 子集
  （标题、列表、引用、表格、可复制的代码块、链接），从不拼接 HTML 字符串。
- 输入框编辑会话的共享草稿。设备的语音草稿会出现在输入框中；设备录音时，状态行
  显示实时转写。发送时先让草稿与输入框内容一致（在已核对的修订号上编辑、追加或
  删除分段），再发送该修订号；草稿仍在录音时提示忙碌。新对话在首次发送时创建会话，
  并以首条内容命名。
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

Go 广播 `_voiceagent._tcp.local`，携带 `ver=1`、`path=/api/v1`、能力和
IPv4 备用地址。仅监听回环地址时跳过 mDNS。广播失败会记录日志，HTTP 服务
继续可用。设备发现后验证 `/api/v1/ping` 并读取 `/api/v1/server`。

- 会话、段落和消息标识采用小写 UUID 格式。
- 元数据每页默认 4 条，支持 `limit=1..20` 和偏移。`has_more` 表示是否还有下一页。
  草稿还提供同一事务读取的 `total` 段落数，便于直接定位最后一页。
- 草稿最多 64 段。各段原文字节总量与包含拼接空行的最终文本均受 64 KiB 限制，
  新增、编辑与转写更新超出任一限额时回滚。待确认替换单独受 64 KiB 限制。
- 实时转写及可选窗口读取使用 UTF-8 字节偏移，每个窗口最多 2048 字节。
  完整消息正文接口返回整条已保存内容。
- 每个设备 SSE 帧最多 1024 字节，增量文字最多 64 字节。
- 每条过滤后的回复最多 2 MiB，上游 SSE 过滤后事件上限为 256 KiB。
  普通文字事件过大时，明确转入结果未确认状态。
- 最多同时接收四条音频和 64 个 SSE 订阅。音频时长上限为五分钟，完整上传／转写
  请求有六分钟预算，用于连接准备、传输收尾和最终识别。聊天超时为十分钟。

## 会话

`POST /api/v1/sessions` 接受 `{"id":"<uuid>","title":"想法"}`。
联系 Hermes 前先持久化创建记录。就绪后返回 201，创建尚未落实时返回 202 和
`state: "creating"`。后台恢复复用同一个上游 ID，Hermes 返回 409 时核对已有
API 会话。Go 保存会话标题，并在创建 Hermes 会话时转发标题。
如果 Hermes 返回 `400 invalid_title`，则保持同一会话 ID，省略标题重试一次。
设备继续显示本地标题，Hermes 使用默认标题。其他创建错误保留待恢复状态。改名会核对上游
归属、更新 Hermes，再更新本地标题。上游失败时保留本地原名，供用户明确重试。
该索引覆盖通过 Go 创建的会话。
会话处于创建中时，仍可创建草稿并进行转写，发送等待上游会话就绪。
因此，使用 fake ASR、上游凭据留空时，也可以在本地开发草稿流程。

`GET /api/v1/sessions` 返回本地会话索引。对 `/api/v1/sessions/{id}`
使用 `GET` 读取元数据，使用 `PATCH` 修改 Hermes 与本地标题。`DELETE` 将状态改为
删除中，协调上游删除后保留已退役的本地 ID。活动或结果未确认的消息会返回忙碌。
删除前核对上游会话的 API 来源。

## 草稿与音频

网关常驻一个已配置好的 OpenAI 实时转写会话。OpenAI 实时转写指南在同一会话中发送多段音频：
每段结束时提交，并按 `item_id` 匹配转写事件，因为不同段的完成顺序不保证。每次录音先发送
`input_audio_buffer.clear`，追加音频后只提交一次，并只接受清空后的第一个条目。正常结束的
会话放回池中；失败或取消的会话直接丢弃，因为其中可能残留音频或进行中的条目。每个连接由
专用读协程处理每 20 秒一次的保活 ping，断线立即发现并以最长 60 秒的指数退避重连。会话
在 50 分钟时轮换，低于文档所列的实时会话时长上限。同时进行的第二段录音会另开会话。

创建录音分段（空分段或替换分段）时，网关在返回 `201` 前预留常驻会话（没有则新建），
最多等待 3 秒，低于设备 4 秒的请求超时。设备在收到响应前保持“准备录音”动画，
因此录音开始时转写会话已经就绪。预留会话归属该分段 ID，只交给一次上传，闲置 30 秒
或网关停止时放回池中。日志记录准备耗时和转写失败代码，不含凭据。

握手超过 3 秒或上传没有预备会话时，音频上传同样从第一个字节起即被读取，与 OpenAI
实时会话建立同时进行。经代理冷连接握手约需
2.5 秒；此前网关在收到 `session.updated` 前不读取请求体，TCP 背压使设备 8 KiB PCM
队列在 2.8–2.9 秒溢出。预读缓冲每次录音最多 16 MiB，超过 300 秒 16 kHz 完整录音，
按实际到达的数据分块分配；请求本身的字节上限仍然生效。

`GET /api/v1/sessions/{id}/draft` 返回一致的草稿版本与一页元数据，
每项包括段落标识、状态、长度和预览。
`GET /api/v1/sessions/{id}/draft/text` 通过相同的有界 UTF-8 窗口接口读取合并草稿和
事务一致的版本，段落顺序及空行拼接规则与发送时的固定快照一致。

`POST /api/v1/sessions/{id}/segments` 追加
`{"id":"<segment-uuid>","revision":1,"text":"第一段想法"}`，
返回新的草稿版本。文字为空时创建待录音段落。对 `/segments/{segment}`
使用 `PATCH`，提交 `revision` 和替换后的 `text`，使用 `DELETE` 提交
已审阅的 `revision`。编辑会使旧识别代次退役，并取消对应的活动录音。

暂存重录先用空文字及 `{"id":"<segment-uuid>","revision":1,"replace":true}`
创建段落，预约唯一的临时替换槽，即使普通草稿已达 64 段也可重录。响应丢失时
使用相同 ID 和创建版本重试，随后以 `replace=1&defer_commit=1` 上传，并对同一
ID 提交或取消。预约期间等待完成后才能修改或发送旧草稿。未开始的预约一分钟后
过期，服务重启时退役。元数据在此期间最多 65 条，普通追加仍限制为 64 段。

`POST /api/v1/sessions/{id}/segments/{segment}/audio?revision=<n>&sample_rate=16000`
接收原始单声道、小端 PCM16，使用 `Content-Type: audio/pcm`。
输入采样率支持 16000 和 24000，允许分块请求，EOF 表示提交本段音频。
Go 增量地将 16 kHz 输入重采样为 24 kHz，并连接 OpenAI。响应成功表示转写
已落实到草稿。上传期间持久化临时文字，发布 `draft.updated` 事件。
音频字节按流处理，草稿文字持久保存。

音频上传添加 `replace=1` 后用于重录待发送草稿。新转写成功且非空时，在保存最终
文字的同一事务中退役此前的待发送段落。在此之前保留原文。录音失败且存在此前
草稿时，退役本次失败的替换段落。`replace=0` 或省略该参数时继续追加段落。
设备的直接发送交互使用暂存替换模式。

使用 `replace=1&defer_commit=1` 时，音频完成返回
`{"status":"ready","revision":<n>}`，段落保存为 `staged`，每次暂存操作使用新建的空段落 ID，供审阅的文字仍为旧稿。
设备自动向 `POST /api/v1/sessions/{id}/segments/{segment}/commit` 提交
`{"revision":<n>}`，事务内替换旧稿。同体重试返回首次提交的输出版本。
`POST /api/v1/sessions/{id}/segments/{segment}/abort` 无需请求体，返回
`{"status":"cancelled"|"committed","revision":<n>}`，已提交的结果只报告其状态。
提交与取消通过同一事务边界判胜。待确认替换期间锁住同会话的其他编辑及录音，
发送需等待录音与确认处理完毕。

暂存内容从合并草稿中隔离，重启或过期后丢弃。独立清理任务每 10 秒检查一次，
清理已就绪至少 60 秒的暂存结果，每轮最多处理 64 个会话。追加模式的失败部分
转写可继续审阅。录音模式与提交身份持久化在 SQLite 中。结构 v3 新增设备诊断报告表；v2 数据库原地迁移，新数据库直接创建为 v3。
用户授权清除旧会话后，早期 v1 迁移路径已移除，不支持的数据库版本会明确拒绝。

`GET /api/v1/sessions/{id}/segments/{segment}/text` 读取文字窗口，使用
`offset`、`limit` 和可选的 `revision`。请求版本发生变化时返回 409。
识别、编辑和发送均推进草稿版本。文字窗口还支持 `before=<UTF-8字节边界>`
读取之前的窗口，以及 `tail=1` 读取最新末尾文字，这些选择方式与 `offset` 互斥。
0.2.0 服务声明 `bounded_paging`、`text_before`、`text_tail`、`session_titles`、
`draft_text`、`replace_recording`、`staged_recording` 和 `replacement_reservation`，
设备连接前会验证自身依赖的能力。

## 完整消息

设备和管理页通过 `GET /api/v1/sessions/{id}/turn` 选择完整的一次用户提问／助手回复。
响应包含 `message` 元数据以及 `before`／`after` 消息 ID。尚未发送消息的会话返回
`{"message":null,"before":"","after":""}`。历史读取通过 `message=<uuid>`、
`before=<uuid>` 或 `after=<uuid>` 选择其中一个参数。相邻消息查询限定在当前会话内，
到达历史边界时返回 404。无效或重复的选择参数返回 400。

通过 `GET /api/v1/sessions/{id}/messages/{message}/content?part=input|reply`
读取完整 UTF-8 正文。纯文本响应包含精确的字节长度 `Content-Length`、`X-Revision`、
`X-Message-State` 和 `Cache-Control: no-store`。正文保留原始换行及排版。完整消息
响应使用消息级标识。通过 `GET /api/v1/sessions/{id}/draft/content` 读取完整待发送
草稿，`X-Revision` 返回对应草稿版本。

可选 `revision=<n>` 要求精确版本，版本推进后返回 409。流式恢复时先订阅 SSE，再读取
当前完整回复，以返回正文的字节长度建立增量基线。响应头中的版本及状态对应同一次
持久化快照。设备在 RAM 中保留当前输入和回复，访问历史时加载另一轮完整消息。
能力响应声明 `whole_messages` 与 `reply_sse`。

## 对话窗口

`GET /api/v1/sessions/{id}/conversation` 最多返回四块按时间排列的文字，默认位于
最新回复末尾。每块包含消息标识、`part`（`input` 或 `reply`）、状态、版本、
UTF-8 字节边界及最多 256 字节文字。正在生成且暂为空的回复保留助手卡片。
响应提供 `before`、`after` 与 `start` 游标，选择一个分别通过 `before=<游标>`、
`after=<游标>` 或 `from=<游标>` 翻页或刷新。相邻页共享一块以保持可见滚动位置。
游标仅在选中的会话中读取。文字边界过期时返回 409，客户端保留当前内容并读取
新窗口。对应能力标识为 `conversation_windows`。

## 明确发送与回复

`POST /api/v1/sessions/{id}/messages` 接受
`{"id":"<message-uuid>","revision":<已审阅草稿版本>}`。
同一事务中完成版本校验、按顺序合并段落、持久化固定消息，并将这些段落从草稿
退役，返回 202。相同 ID 和审阅版本的重复请求返回同一逻辑消息。
版本过期时返回 409，供用户重新审阅当前草稿。
草稿中仍有活动录音段落时，发送返回忙碌状态。

后台任务通过会话 ID 和稳定的幂等键，向 Hermes Chat Completions 提交一条
新的用户消息。保存模型、输入和上游身份摘要，保持消息关联。
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

## 设备事件流

`GET /api/v1/events?session_id=<uuid>` 订阅一个会话，省略筛选参数时订阅
全局事件。连接首先发送 `resync`，客户端读取当前整条消息元数据及完整正文建立基线。
事件包括 `session.updated`、`draft.updated`、`chat.delta`、`chat.reset`
和 `message.updated`。聊天增量携带 UTF-8 字节偏移，零偏移可能由 JSON 编码省略。
快照已包含的增量按偏移去重；偏移不连续或收到 `chat.reset` 时重新读取完整回复。
`message.updated` 将状态与持久化完整快照对齐。每十秒发送保活。

慢订阅者断开后，通过重连核对状态。事件丢失后的恢复依据是持久化元数据与完整消息快照，
SSE 投递本身是临时的。审批以待处理状态呈现，详细审批控制留给后续产品决策。

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

`go test -race ./...` 覆盖 SQLite 草稿与发送事务、并发、识别退役、重启恢复、
UTF-8 窗口、流式 PCM、模拟 OpenAI WebSocket、Hermes HTTP/SSE 和媒体过滤。
`tools/smoke-server.sh` 用真实二进制与本地模拟上游，验证编辑、发送和重启持久化。

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
