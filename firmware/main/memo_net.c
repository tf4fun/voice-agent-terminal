// main/memo_net.c —— 常驻联网：STA 连接、重连退避、SNTP 对时。
#include "memo_net.h"
#include "memo_network_info_store.h"

#include <stdio.h>
#include <string.h>
#include <stdatomic.h>
#include <time.h>

#include "esp_event.h"
#include "esp_log.h"
#include "esp_netif.h"
#include "esp_sntp.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "freertos/FreeRTOS.h"

static const char *TAG = "memo_net";

/* 2020-01-01：早于它只可能是"没对过时"。 */
#define MEMO_EPOCH_FLOOR 1577836800ULL

/* 重连退避：2s 起步，翻倍到 60s 封顶。 */
#define RECONNECT_MIN_MS 2000
#define RECONNECT_MAX_MS 60000

static _Atomic memo_net_state_t s_state = MEMO_NET_OFF;
static memo_network_info_store_t s_info = MEMO_NETWORK_INFO_STORE_INIT;
static esp_netif_t *s_netif;
static esp_event_handler_instance_t s_wifi_handler;
static esp_event_handler_instance_t s_ip_handler;
static esp_timer_handle_t s_retry_timer;
static atomic_uint s_backoff_ms = RECONNECT_MIN_MS;
static atomic_bool s_started;
static bool s_wifi_initialized;
static bool s_wifi_stopped;
static bool s_release_pending;
static uint32_t s_session; // Protected with s_info.lock, including event publication.
static bool s_wifi_handler_registered, s_ip_handler_registered;
static atomic_bool s_time_synced;
/* Running and synchronised are different: an unreachable NTP server can leave
 * the client running across many Wi-Fi reconnects without ever syncing. */
static bool s_sntp_started;

/* Event formatting/driver calls stay outside the critical section. A callback
 * that began before suspend cannot publish after that session was released. */
static bool event_session(uint32_t *session)
{
    portENTER_CRITICAL(&s_info.lock);
    const bool valid = s_started;
    *session = s_session;
    portEXIT_CRITICAL(&s_info.lock);
    return valid;
}

static bool publish_event(uint32_t session, memo_net_state_t state, const char *ip)
{
    portENTER_CRITICAL(&s_info.lock);
    const bool valid = s_started && session == s_session;
    if (valid) {
        memset(s_info.value.ip, 0, sizeof(s_info.value.ip));
        if (ip != NULL) memcpy(s_info.value.ip, ip, strnlen(ip, sizeof(s_info.value.ip)-1));
        s_state = state;
        if (state == MEMO_NET_CONNECTED) s_backoff_ms = RECONNECT_MIN_MS;
    }
    portEXIT_CRITICAL(&s_info.lock);
    return valid;
}

static void schedule_retry(void)
{
    if (!s_started || s_retry_timer == NULL) return;
    esp_timer_stop(s_retry_timer);
    esp_timer_start_once(s_retry_timer, (uint64_t)s_backoff_ms * 1000);
    s_backoff_ms = s_backoff_ms * 2 > RECONNECT_MAX_MS ? RECONNECT_MAX_MS : s_backoff_ms * 2;
}

static void retry_cb(void *arg)
{
    (void)arg;
    if (!s_started || s_state != MEMO_NET_CONNECTING) return;
    esp_err_t err = esp_wifi_connect();
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "connect: %s", esp_err_to_name(err));
        schedule_retry();
    }
}

static void start_sntp(void)
{
    if (s_time_synced || s_sntp_started) return;
    s_sntp_started = true; // Also covers init queued to lwIP but not executed yet.
    esp_sntp_setoperatingmode(ESP_SNTP_OPMODE_POLL);
    esp_sntp_setservername(0, "pool.ntp.org");
    esp_sntp_init();
    ESP_LOGI(TAG, "SNTP started");
}

static void wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    (void)arg;
    (void)base;
    (void)data;
    uint32_t session;
    if (!event_session(&session)) return;
    if (id == WIFI_EVENT_STA_START) {
        if (publish_event(session, MEMO_NET_CONNECTING, NULL)) esp_wifi_connect();
    } else if (id == WIFI_EVENT_STA_DISCONNECTED) {
        if (!publish_event(session, MEMO_NET_CONNECTING, NULL)) return;
        ESP_LOGW(TAG, "disconnected, retry in %u ms", (unsigned)s_backoff_ms);
        schedule_retry();
    }
}

static void ip_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    (void)arg;
    (void)base;
    uint32_t session;
    if (id != IP_EVENT_STA_GOT_IP || !event_session(&session)) return;

    const ip_event_got_ip_t *event = (const ip_event_got_ip_t *)data;
    char ip[16];
    snprintf(ip, sizeof(ip), IPSTR, IP2STR(&event->ip_info.ip));
    if (!publish_event(session, MEMO_NET_CONNECTED, ip)) return;
    ESP_LOGI(TAG, "station connected");
    start_sntp();
}

static esp_err_t start_failed(esp_err_t error)
{
    portENTER_CRITICAL(&s_info.lock);
    s_started = false;
    s_session++;
    s_info.value.ip[0] = '\0';
    s_state = MEMO_NET_FAILED;
    portEXIT_CRITICAL(&s_info.lock);
    ESP_LOGW(TAG, "station start failed: %s", esp_err_to_name(error));
    return error;
}

esp_err_t memo_net_start(void)
{
    /* Connection events and the retry timer own reconnection. A repeated
     * application start must not revoke a live IP or duplicate connect. */
    if (s_started) return ESP_OK;
    if (s_release_pending) {
        const esp_err_t cleanup = memo_net_suspend();
        if (cleanup != ESP_OK) return cleanup;
    }
    s_state = MEMO_NET_CONNECTING;
    memo_network_info_ip(&s_info, NULL);

    esp_err_t err = esp_netif_init();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) {
        ESP_LOGE(TAG, "netif init: %s", esp_err_to_name(err));
        return start_failed(err);
    }
    err = esp_event_loop_create_default();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) {
        ESP_LOGE(TAG, "event loop: %s", esp_err_to_name(err));
        return start_failed(err);
    }

    if (s_netif == NULL) {
        s_netif = esp_netif_create_default_wifi_sta();
        if (s_netif == NULL) {
            ESP_LOGE(TAG, "cannot create station netif");
            return start_failed(ESP_FAIL);
        }
    }

    if (!s_wifi_initialized) {
        wifi_init_config_t init_cfg = WIFI_INIT_CONFIG_DEFAULT();
        err = esp_wifi_init(&init_cfg);
        if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) return start_failed(err);
        s_wifi_initialized = true;
        s_wifi_stopped = true;
    }
    if (!s_wifi_handler_registered) {
        err = esp_event_handler_instance_register(WIFI_EVENT, ESP_EVENT_ANY_ID,
                                                  wifi_event, NULL, &s_wifi_handler);
        if (err != ESP_OK) return start_failed(err);
        s_wifi_handler_registered = true;
    }
    if (!s_ip_handler_registered) {
        err = esp_event_handler_instance_register(IP_EVENT, IP_EVENT_STA_GOT_IP,
                                                  ip_event, NULL, &s_ip_handler);
        if (err != ESP_OK) return start_failed(err);
        s_ip_handler_registered = true;
    }

    if (s_retry_timer == NULL) {
        const esp_timer_create_args_t args = {.callback = retry_cb, .name = "memo_net_retry"};
        err = esp_timer_create(&args, &s_retry_timer);
        if (err != ESP_OK) return start_failed(err);
    }

    /* 凭据由配网写入；这里只读取，不覆盖。 */
    wifi_config_t cfg = {0};
    err = esp_wifi_get_config(WIFI_IF_STA, &cfg);
    if (err != ESP_OK) return start_failed(err);
    const size_t ssid_len = strnlen((const char *)cfg.sta.ssid, sizeof(cfg.sta.ssid));
    memo_network_info_ssid(&s_info, cfg.sta.ssid, ssid_len);
    if (ssid_len == 0) {
        s_state = MEMO_NET_NO_CREDENTIALS;
        ESP_LOGI(TAG, "no stored credentials; connect via Bluetooth provisioning");
        return ESP_OK;
    }

    err = esp_wifi_set_mode(WIFI_MODE_STA);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "set mode: %s", esp_err_to_name(err));
        return start_failed(err);
    }
    s_backoff_ms = RECONNECT_MIN_MS;
    portENTER_CRITICAL(&s_info.lock);
    s_session++;
    s_started = true;
    portEXIT_CRITICAL(&s_info.lock);
    s_wifi_stopped = false;
    err = esp_wifi_start();
    if (err != ESP_OK) {
        s_release_pending = true;
        ESP_LOGE(TAG, "wifi start: %s", esp_err_to_name(err));
        return start_failed(err);
    }
    if (esp_wifi_set_ps(WIFI_PS_MIN_MODEM) != ESP_OK) {
        ESP_LOGW(TAG, "cannot enable modem sleep");
    }

    ESP_LOGI(TAG, "starting STA");
    return ESP_OK;
}

esp_err_t memo_net_suspend(void)
{
    portENTER_CRITICAL(&s_info.lock);
    s_started = false;
    s_session++;
    s_info.value.ip[0] = '\0';
    s_state = MEMO_NET_OFF;
    portEXIT_CRITICAL(&s_info.lock);
    if (s_retry_timer != NULL) esp_timer_stop(s_retry_timer);
    if (s_wifi_initialized) {
        s_release_pending = true;
        if (!s_wifi_stopped) {
            const esp_err_t stop = esp_wifi_stop();
            if (stop == ESP_ERR_WIFI_NOT_INIT) {
                s_wifi_initialized = false;
            } else if (stop != ESP_OK) return start_failed(stop);
            s_wifi_stopped = true;
        }
        if (s_wifi_initialized) {
            const esp_err_t deinit = esp_wifi_deinit();
            if (deinit != ESP_OK && deinit != ESP_ERR_WIFI_NOT_INIT) return start_failed(deinit);
            s_wifi_initialized = false;
        }
    }
    s_release_pending = false;
    ESP_LOGI(TAG, "WiFi suspended, free heap %u", (unsigned)esp_get_free_heap_size());
    return ESP_OK;
}

memo_net_state_t memo_net_state(void)
{
    return s_state;
}

void memo_net_info(memo_network_info_t *out)
{
    memo_network_info_copy(&s_info, out);
}

bool memo_net_has_ip(void)
{
    return s_state == MEMO_NET_CONNECTED;
}

bool memo_net_time_synced(void)
{
    if (!s_time_synced) {
        /* 轮询一次：SNTP 同步成功后 sntp_get_sync_status 会变成 COMPLETED。 */
        if (esp_sntp_get_sync_status() == SNTP_SYNC_STATUS_COMPLETED) {
            s_time_synced = true;
        }
    }
    return s_time_synced;
}

uint64_t memo_net_now(void)
{
    const time_t now = time(NULL);
    if (now > (time_t)MEMO_EPOCH_FLOOR) return (uint64_t)now;
    return 0;
}
