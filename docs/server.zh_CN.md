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

## 发现与限制

Go 广播 `_voiceagent._tcp.local`，携带 `ver=1`、`path=/api/v1`、能力和
IPv4 备用地址。仅监听回环地址时跳过 mDNS。广播失败会记录日志，HTTP 服务
继续可用。设备发现后验证 `/api/v1/ping` 并读取 `/api/v1/server`。

- 会话、段落和消息标识采用小写 UUID 格式。
- 元数据每页默认 4 条，支持 `limit=1..20` 和偏移。
- 草稿最多 64 段、64 KiB 文字，合并时插入空行。
- 文字按 UTF-8 字节偏移读取，每个窗口最多 2048 字节。
- 每个设备 SSE 帧最多 1024 字节，增量文字最多 64 字节。
- 每条过滤后的回复最多 2 MiB，上游 SSE 过滤后事件上限为 256 KiB。
  普通文字事件过大时，明确转入结果未确认状态。
- 最多同时接收四条音频和 64 个 SSE 订阅。录音超时与音频时长上限均为五分钟，
  聊天超时为十分钟。

## 会话

`POST /api/v1/sessions` 接受 `{"id":"<uuid>","title":"想法"}`。
联系 Hermes 前先持久化创建记录。就绪后返回 201，创建尚未落实时返回 202 和
`state: "creating"`。后台恢复复用同一个上游 ID，Hermes 返回 409 时核对已有
API 会话。会话标题是保存在 Go 中的设备显示名称。
会话处于创建中时，仍可创建草稿并进行转写，发送等待上游会话就绪。
因此，使用 fake ASR、上游凭据留空时，也可以在本地开发草稿流程。

`GET /api/v1/sessions` 返回本地会话索引。对 `/api/v1/sessions/{id}`
使用 `GET` 读取元数据，使用 `PATCH` 修改本地标题。`DELETE` 将状态改为
删除中，协调上游删除后保留已退役的本地 ID。活动或结果未确认的消息会返回忙碌。
删除前核对上游会话的 API 来源。

## 草稿与音频

`GET /api/v1/sessions/{id}/draft` 返回一致的草稿版本与一页元数据，
每项包括段落标识、状态、长度和预览。

`POST /api/v1/sessions/{id}/segments` 追加
`{"id":"<segment-uuid>","revision":1,"text":"第一段想法"}`，
返回新的草稿版本。文字为空时创建待录音段落。对 `/segments/{segment}`
使用 `PATCH`，提交 `revision` 和替换后的 `text`，使用 `DELETE` 提交
已审阅的 `revision`。编辑会使旧识别代次退役，并取消对应的活动录音。

`POST /api/v1/sessions/{id}/segments/{segment}/audio?revision=<n>&sample_rate=16000`
接收原始单声道、小端 PCM16，使用 `Content-Type: audio/pcm`。
输入采样率支持 16000 和 24000，允许分块请求，EOF 表示提交本段音频。
Go 增量地将 16 kHz 输入重采样为 24 kHz，并连接 OpenAI。响应成功表示转写
已落实到草稿。上传期间持久化临时文字，发布 `draft.updated` 事件。
音频字节按流处理，草稿文字持久保存。设备传输方案确定后可增加编码音频接收。

`GET /api/v1/sessions/{id}/segments/{segment}/text` 读取文字窗口，使用
`offset`、`limit` 和可选的 `revision`。请求版本发生变化时返回 409。
识别、编辑和发送均推进草稿版本。

## 明确发送与回复

`POST /api/v1/sessions/{id}/messages` 接受
`{"id":"<message-uuid>","revision":<已审阅草稿版本>}`。
同一事务中完成版本校验、按顺序合并段落、持久化固定消息，并将这些段落从草稿
退役，返回 202。相同 ID 和审阅版本的重复请求返回同一逻辑消息。
版本过期时返回 409，供用户重新审阅当前草稿。

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
上游地址或凭据变化时，保留原身份范围并阻止向新身份提交。
此恢复面向 Hermes 的 canonical 持久接收模式，真实使用前需要验证部署模式
的幂等语义。Legacy 模式的流式重试保证有所差异。

## 设备事件流

`GET /api/v1/events?session_id=<uuid>` 订阅一个会话，省略筛选参数时订阅
全局事件。连接首先发送 `resync`，客户端读取当前元数据和文字窗口建立基线。
事件包括 `session.updated`、`draft.updated`、`chat.delta`、`chat.reset`
和 `message.updated`。聊天增量携带字节偏移，重置事件要求重新读取回复窗口。
每十秒发送保活。

慢订阅者断开后，通过重连核对状态。事件丢失后的恢复依据是持久化元数据与窗口，
SSE 投递本身是临时的。审批以待处理状态呈现，详细审批控制留给后续产品决策。

## 测试与构建产物

`go test -race ./...` 覆盖 SQLite 草稿与发送事务、并发、识别退役、重启恢复、
UTF-8 窗口、流式 PCM、模拟 OpenAI WebSocket、Hermes HTTP/SSE 和媒体过滤。
`tools/smoke-server.sh` 用真实二进制与本地模拟上游，验证编辑、发送和重启持久化。

`tools/build-server.sh amd64` 与 `arm64` 在 `build/server/linux-<arch>/`
生成 CGO-free Linux 二进制及 SHA256SUMS。GitHub Actions 执行检查并上传
两种架构产物。真实上游凭据、局域网 mDNS、麦克风识别准确性与延迟、设备 RAM
仍需集成验收。
