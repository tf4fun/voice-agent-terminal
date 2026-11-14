// main/memo_net.h —— 常驻 WiFi 站模式连接与对时。
//
// 与配网的分工：memo_provision 只在配网时占用 WiFi/BLE，结束后把控制权交回这里。
// 凭据由 WiFi 驱动存在 NVS，本模块只负责"用已有凭据连上、断线重连、对时"。
#pragma once

#include "esp_err.h"
#include "memo_network_info.h"

#include <stdbool.h>
#include <stdint.h>

typedef enum {
    MEMO_NET_OFF = 0,      /* 未启动（或配网期间被挂起） */
    MEMO_NET_NO_CREDENTIALS,
    MEMO_NET_CONNECTING,
    MEMO_NET_CONNECTED,
    MEMO_NET_FAILED,
} memo_net_state_t;

/* 初始化 WiFi 并用 NVS 里的凭据开始连接。可重复调用（例如配网结束后）。 */
esp_err_t memo_net_start(void);

/* 配网要接管 WiFi 时先挂起本模块；netif 会移交给配网，避免重复创建默认 STA。 */
/* Failed stop/deinit retains driver ownership. No other Wi-Fi owner may start
 * until this returns ESP_OK. A later start retries pending release first. */
esp_err_t memo_net_suspend(void);


memo_net_state_t memo_net_state(void);
/* Caller-owned copy; no mutable strings escape event callbacks. */
void memo_net_info(memo_network_info_t *out);
bool memo_net_has_ip(void);

/* SNTP 是否已经同步过（决定 recorded_at 能不能用真实时间）。 */
bool memo_net_time_synced(void);

/* 把时间戳规整成"可信的 Unix 秒"，未对时返回 0。 */
uint64_t memo_net_now(void);
