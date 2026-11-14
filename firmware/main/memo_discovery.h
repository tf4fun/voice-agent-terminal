// main/memo_discovery.h —— 通过 mDNS 找到局域网里的服务端。
//
// 服务端广播 `_voiceagent._tcp`；这里查 PTR → SRV → A，得到 host:port 后交给上传模块。
#pragma once

#include "esp_err.h"

#include <stdbool.h>
#include <stdint.h>

#define MEMO_DISCOVERY_SERVICE "_voiceagent"
#define MEMO_DISCOVERY_PROTO "_tcp"

typedef enum {
    MEMO_DISCOVERY_IDLE = 0,
    MEMO_DISCOVERY_SEARCHING,
    MEMO_DISCOVERY_FOUND,
    MEMO_DISCOVERY_CANDIDATE,
    MEMO_DISCOVERY_FAILED,
} memo_discovery_state_t;

/* 启动 mDNS 客户端（幂等）。 */
esp_err_t memo_discovery_init(void);

/* Search PTR/SRV/A with a per-query timeout. After invalidate, try the next
 * endpoint (including other IPv4 addresses of an instance), wrapping when needed.
 * Results are released before returning; obtain the candidate with endpoint(). */
esp_err_t memo_discovery_search(uint32_t timeout_ms);

/* 释放 mDNS 占用的内存（约十几 KB）。下次 search 会自动重新初始化。 */
void memo_discovery_stop(void);

memo_discovery_state_t memo_discovery_state(void);
typedef struct {
    char host[64];
    uint16_t port;
    uint32_t generation;
    bool confirmed;
} memo_endpoint_t;
/* Copy address/port together; no shared mutable string escapes the lock. */
bool memo_discovery_endpoint(memo_endpoint_t *out);
void memo_discovery_confirm(const memo_endpoint_t *endpoint);
void memo_discovery_invalidate(const memo_endpoint_t *endpoint);
