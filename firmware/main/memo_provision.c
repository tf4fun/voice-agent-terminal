// main/memo_provision.c —— BLUFI over NimBLE 配网。
//
// 结构取自上游 demo/blufi-provisioning 分支（main/demo_blufi.c 与
// memo_provision_security_security.c），保留其 Wi-Fi/NimBLE 生命周期与安全回调，只把 LCD
// 页面换成由本模块对外暴露的状态查询，界面交给 memo_ui_provision 绘制。
#include "memo_provision.h"
#include "memo_network_info_store.h"

#include "memo_provision_security.h"

#include "esp_blufi.h"
#include "esp_blufi_api.h"
#include "esp_event.h"
#include "esp_log.h"
#include "esp_heap_caps.h"
#include "esp_system.h"
#include "esp_netif.h"
#include "nvs_flash.h"
#include "esp_wifi.h"
#include "esp_wifi_default.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "host/ble_hs.h"
#include "lwip/ip4_addr.h"
#include "nimble/nimble_port.h"
#include "nimble/nimble_port_freertos.h"
#include "services/gap/ble_svc_gap.h"
#include <string.h>
#include <stdatomic.h>

static const char *TAG = "memo_prov";
// ESP Config 微信小程序默认只显示以 "BLUFI" 开头的设备。
static const char *DEVICE_NAME = MEMO_PROV_DEVICE_NAME;

#define BLUFI_AP_LIST_COUNT 16

typedef memo_provision_state_t blufi_state_t;

static esp_netif_t *s_sta_netif;
static esp_event_handler_instance_t s_wifi_handler;
static esp_event_handler_instance_t s_ip_handler;
static wifi_config_t s_sta_config;
static uint32_t s_config_revision;
static _Atomic blufi_state_t s_state;
static _Atomic esp_err_t s_error;
static memo_network_info_store_t s_info = MEMO_NETWORK_INFO_STORE_INIT;
static portMUX_TYPE s_config_lock = portMUX_INITIALIZER_UNLOCKED;
static atomic_bool s_wifi_initialized;
static atomic_bool s_wifi_started;
static atomic_bool s_wifi_stop_needed;
static atomic_bool s_wifi_handler_registered;
static atomic_bool s_ip_handler_registered;
static atomic_bool s_host_initialized;
static bool s_port_initialized;
static atomic_bool s_host_running;
static atomic_bool s_gatt_initialized;
static atomic_bool s_btc_initialized;
static atomic_bool s_profile_initialized;
static atomic_bool s_ble_connected;
static atomic_bool s_wifi_connecting;
static atomic_bool s_wifi_got_ip;
static atomic_bool s_reconnect_after_disconnect;
static SemaphoreHandle_t s_host_stopped;
static SemaphoreHandle_t s_profile_stopped;
static bool s_profile_stop_requested;
static _Atomic esp_err_t s_profile_stop_error;
static atomic_bool s_accept_callbacks;
static bool s_host_stop_requested;

static esp_err_t wifi_start(void);

/* Only bounded copies under the critical section; driver/BLE calls stay outside. */
static void config_copy(wifi_config_t *out, uint32_t *revision)
{
    portENTER_CRITICAL(&s_config_lock);
    *out = s_sta_config;
    if (revision != NULL) *revision = s_config_revision;
    portEXIT_CRITICAL(&s_config_lock);
}

static bool config_store(const wifi_config_t *cfg, uint32_t revision, bool conditional)
{
    portENTER_CRITICAL(&s_config_lock);
    if (conditional && s_config_revision != revision) {
        portEXIT_CRITICAL(&s_config_lock);
        return false;
    }
    s_sta_config = *cfg;
    s_config_revision++;
    // Consistent lock order: config -> display. Both contain bounded copies;
    // publishing outside this lock could put an older SSID over a new one.
    memo_network_info_ssid(&s_info, cfg->sta.ssid, sizeof(cfg->sta.ssid));
    portEXIT_CRITICAL(&s_config_lock);
    return true;
}

static void apply_config(const wifi_config_t *cfg)
{
    wifi_config_t local = *cfg;
    const esp_err_t err = esp_wifi_set_config(WIFI_IF_STA, &local);
    if (err == ESP_OK) config_store(cfg, 0, false);
    else {
        s_error = err;
        s_state = MEMO_PROV_FAILED;
    }
}

/* 收到凭据或要求扫描时才启动 WiFi 驱动。 */
static esp_err_t ensure_wifi(void)
{
    if (s_wifi_started) return ESP_OK;
    const esp_err_t err = wifi_start();
    ESP_LOGI(TAG, "lazy wifi_start: %s, free heap %u", esp_err_to_name(err),
             (unsigned)esp_get_free_heap_size());
    return err;
}

static void send_wifi_report(esp_blufi_sta_conn_state_t state)
{
    wifi_mode_t mode = WIFI_MODE_STA;
    esp_wifi_get_mode(&mode);
    esp_blufi_extra_info_t info = { 0 };
    wifi_config_t cfg;
    config_copy(&cfg, NULL);
    size_t ssid_len = strnlen((const char *)cfg.sta.ssid, sizeof(cfg.sta.ssid));
    if (ssid_len > 0) {
        info.sta_ssid = cfg.sta.ssid;
        info.sta_ssid_len = ssid_len;
    }
    esp_blufi_send_wifi_conn_report(mode, state, 0, &info);
}

static void send_wifi_list(void)
{
    uint16_t count = BLUFI_AP_LIST_COUNT;
    wifi_ap_record_t records[BLUFI_AP_LIST_COUNT] = { 0 };
    esp_blufi_ap_record_t list[BLUFI_AP_LIST_COUNT] = { 0 };
    esp_err_t err = esp_wifi_scan_get_ap_records(&count, records);
    if (err != ESP_OK) {
        esp_blufi_send_error_info(ESP_BLUFI_WIFI_SCAN_FAIL);
        return;
    }
    for (uint16_t i = 0; i < count; i++) {
        list[i].rssi = records[i].rssi;
        memcpy(list[i].ssid, records[i].ssid, sizeof(list[i].ssid));
    }
    if (s_ble_connected) esp_blufi_send_wifi_list(count, list);
}

static void request_wifi_connect(void)
{
    bool was_connected = s_wifi_got_ip;
    s_wifi_connecting = true;
    s_wifi_got_ip = false;
    s_state = MEMO_PROV_WIFI_CONNECTING;
    if (was_connected) {
        s_reconnect_after_disconnect = true;
        if (esp_wifi_disconnect() == ESP_OK) return;
        s_reconnect_after_disconnect = false;
    }
    esp_err_t err = esp_wifi_connect();
    if (err != ESP_OK) {
        s_error = err;
        s_wifi_connecting = false;
        s_state = MEMO_PROV_FAILED;
    }
}

static void wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    (void)arg;
    (void)base;
    if (!s_accept_callbacks) return;
    if (id == WIFI_EVENT_STA_START) {
        wifi_config_t cfg;
        uint32_t revision;
        config_copy(&cfg, &revision);
        const esp_err_t err = esp_wifi_get_config(WIFI_IF_STA, &cfg);
        if (err != ESP_OK) { s_error = err; s_state = MEMO_PROV_FAILED; return; }
        if (!config_store(&cfg, revision, true)) config_copy(&cfg, NULL);
        if (cfg.sta.ssid[0] != '\0' && !s_wifi_connecting) request_wifi_connect();
    } else if (id == WIFI_EVENT_STA_DISCONNECTED) {
        wifi_event_sta_disconnected_t *event = data;
        ESP_LOGW(TAG, "Wi-Fi disconnected, reason=%u", event->reason);
        if (s_reconnect_after_disconnect) {
            s_reconnect_after_disconnect = false;
            esp_wifi_connect();
            return;
        }
        if (s_state == MEMO_PROV_WIFI_CONNECTING) {
            send_wifi_report(ESP_BLUFI_STA_CONN_FAIL);
        }
        s_wifi_connecting = false;
        s_wifi_got_ip = false;
        s_state = s_ble_connected ? MEMO_PROV_BLE_CONNECTED : MEMO_PROV_ADVERTISING;
        memo_network_info_ip(&s_info, NULL);
    } else if (id == WIFI_EVENT_SCAN_DONE) {
        send_wifi_list();
    }
}

static void ip_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    (void)arg;
    (void)base;
    if (!s_accept_callbacks) return;
    if (id != IP_EVENT_STA_GOT_IP) return;
    ip_event_got_ip_t *event = data;
    char ip[16];
    snprintf(ip, sizeof(ip), IPSTR, IP2STR(&event->ip_info.ip));
    memo_network_info_ip(&s_info, ip);
    s_wifi_connecting = false;
    s_wifi_got_ip = true;
    s_state = MEMO_PROV_WIFI_CONNECTED;
    if (s_ble_connected) send_wifi_report(ESP_BLUFI_STA_CONN_SUCCESS);
}

static void blufi_reset(int reason)
{
    if (!s_accept_callbacks) return;
    ESP_LOGE(TAG, "NimBLE reset: %d", reason);
    s_error = ESP_FAIL;
    s_state = MEMO_PROV_FAILED;
}

static void blufi_sync(void)
{
    if (!s_accept_callbacks) return;
    int rc = esp_blufi_profile_init();
    if (rc == 0) {
        s_profile_initialized = true;
    } else {
        s_error = rc;
        s_state = MEMO_PROV_FAILED;
    }
}

static void host_task(void *arg)
{
    (void)arg;
    nimble_port_run();
    if (s_host_stopped) xSemaphoreGive(s_host_stopped);
    nimble_port_freertos_deinit();
}

static void blufi_event(esp_blufi_cb_event_t event, esp_blufi_cb_param_t *param)
{
    /* Teardown acknowledgement must still reach the owner after ordinary
     * provisioning callbacks have been disabled. Never free BTC before this. */
    if (event == ESP_BLUFI_EVENT_DEINIT_FINISH) {
        s_profile_stop_error = param->deinit_finish.state == ESP_BLUFI_DEINIT_OK ? ESP_OK : ESP_FAIL;
        if (s_profile_stopped) xSemaphoreGive(s_profile_stopped);
        return;
    }
    if (!s_accept_callbacks) return;
    wifi_config_t cfg;
    config_copy(&cfg, NULL);
    switch (event) {
    case ESP_BLUFI_EVENT_INIT_FINISH: {
        esp_blufi_adv_start_with_name(DEVICE_NAME);
        ESP_LOGI(TAG, "INIT_FINISH: advertising as %s, free heap %u",
                 DEVICE_NAME, (unsigned)esp_get_free_heap_size());
        if (s_wifi_got_ip) {
            s_state = MEMO_PROV_WIFI_CONNECTED;
        } else if (s_wifi_connecting) {
            s_state = MEMO_PROV_WIFI_CONNECTING;
        } else {
            s_state = MEMO_PROV_ADVERTISING;
        }
        break;
    }
    case ESP_BLUFI_EVENT_BLE_CONNECT:
        s_ble_connected = true;
        ESP_LOGI(TAG, "phone connected; free heap %u (largest block %u)",
                 (unsigned)esp_get_free_heap_size(),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT));
        esp_blufi_adv_stop();
        if (memo_provision_security_init() != 0) {
            s_error = ESP_ERR_NO_MEM;
            s_state = MEMO_PROV_FAILED;
        } else if (!s_wifi_got_ip) {
            s_state = MEMO_PROV_BLE_CONNECTED;
        }
        break;
    case ESP_BLUFI_EVENT_BLE_DISCONNECT:
        s_ble_connected = false;
        memo_provision_security_deinit();
        esp_blufi_adv_start_with_name(DEVICE_NAME);
        s_state = s_wifi_got_ip ? MEMO_PROV_WIFI_CONNECTED : MEMO_PROV_ADVERTISING;
        break;
    case ESP_BLUFI_EVENT_SET_WIFI_OPMODE:
        esp_wifi_set_mode(WIFI_MODE_STA);
        break;
    case ESP_BLUFI_EVENT_RECV_STA_BSSID:
        memcpy(cfg.sta.bssid, param->sta_bssid.bssid, 6);
        cfg.sta.bssid_set = true;
        apply_config(&cfg);
        break;
    case ESP_BLUFI_EVENT_RECV_STA_SSID:
        if (ensure_wifi() != ESP_OK) {
            esp_blufi_send_error_info(ESP_BLUFI_WIFI_SCAN_FAIL);
            break;
        }
        if (param->sta_ssid.ssid_len < 0 || (size_t)param->sta_ssid.ssid_len > sizeof(cfg.sta.ssid) ||
            (param->sta_ssid.ssid_len > 0 && param->sta_ssid.ssid == NULL)) {
            esp_blufi_send_error_info(ESP_BLUFI_DATA_FORMAT_ERROR);
            break;
        }
        memset(cfg.sta.ssid, 0, sizeof(cfg.sta.ssid));
        memset(cfg.sta.password, 0, sizeof(cfg.sta.password));
        memset(cfg.sta.bssid, 0, sizeof(cfg.sta.bssid));
        cfg.sta.bssid_set = false;
        cfg.sta.threshold.authmode = WIFI_AUTH_OPEN;
        if (param->sta_ssid.ssid_len > 0)
            memcpy(cfg.sta.ssid, param->sta_ssid.ssid, (size_t)param->sta_ssid.ssid_len);
        apply_config(&cfg);
        break;
    case ESP_BLUFI_EVENT_RECV_STA_PASSWD:
        if (ensure_wifi() != ESP_OK) {
            esp_blufi_send_error_info(ESP_BLUFI_WIFI_SCAN_FAIL);
            break;
        }
        if (param->sta_passwd.passwd_len < 0 || (size_t)param->sta_passwd.passwd_len >= sizeof(cfg.sta.password) ||
            (param->sta_passwd.passwd_len > 0 && param->sta_passwd.passwd == NULL)) {
            esp_blufi_send_error_info(ESP_BLUFI_DATA_FORMAT_ERROR);
            break;
        }
        memset(cfg.sta.password, 0, sizeof(cfg.sta.password));
        if (param->sta_passwd.passwd_len > 0)
            memcpy(cfg.sta.password, param->sta_passwd.passwd, (size_t)param->sta_passwd.passwd_len);
        apply_config(&cfg);
        break;
    case ESP_BLUFI_EVENT_REQ_CONNECT_TO_AP:
        request_wifi_connect();
        break;
    case ESP_BLUFI_EVENT_REQ_DISCONNECT_FROM_AP:
        esp_wifi_disconnect();
        break;
    case ESP_BLUFI_EVENT_GET_WIFI_STATUS:
        if (s_wifi_got_ip) {
            send_wifi_report(ESP_BLUFI_STA_CONN_SUCCESS);
        } else if (s_wifi_connecting) {
            send_wifi_report(ESP_BLUFI_STA_CONNECTING);
        } else {
            send_wifi_report(ESP_BLUFI_STA_CONN_FAIL);
        }
        break;
    case ESP_BLUFI_EVENT_GET_WIFI_LIST: {
        if (ensure_wifi() != ESP_OK) {
            esp_blufi_send_error_info(ESP_BLUFI_WIFI_SCAN_FAIL);
            break;
        }
        wifi_scan_config_t config = { 0 };
        if (esp_wifi_scan_start(&config, false) != ESP_OK) {
            esp_blufi_send_error_info(ESP_BLUFI_WIFI_SCAN_FAIL);
        }
        break;
    }
    case ESP_BLUFI_EVENT_RECV_SLAVE_DISCONNECT_BLE:
        esp_blufi_disconnect();
        break;
    case ESP_BLUFI_EVENT_DEAUTHENTICATE_STA:
        esp_wifi_disconnect();
        break;
    case ESP_BLUFI_EVENT_REPORT_ERROR:
        esp_blufi_send_error_info(param->report_error.state);
        break;
    default:
        break;
    }
}

static esp_blufi_callbacks_t s_callbacks = {
    .event_cb = blufi_event,
    .negotiate_data_handler = memo_provision_security_negotiate,
    .encrypt_func = memo_provision_security_encrypt,
    .decrypt_func = memo_provision_security_decrypt,
    .checksum_func = memo_provision_security_checksum,
};

static esp_err_t wifi_start(void)
{
    /* NVS 与默认事件循环由应用启动时准备好（memo_settings_init），这里只做兜底。 */
    esp_err_t err = nvs_flash_init();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) return err;
    err = esp_netif_init();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) return err;
    err = esp_event_loop_create_default();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) return err;

    /* 默认 STA netif 可能已由常驻联网建立：按 ifkey 复用，重复创建会撞
       "duplicate key" 并触发 assert。 */
    s_sta_netif = esp_netif_get_handle_from_ifkey("WIFI_STA_DEF");
    if (s_sta_netif == NULL) {
        s_sta_netif = esp_netif_create_default_wifi_sta();
        if (s_sta_netif == NULL) return ESP_ERR_NO_MEM;
    }
    wifi_init_config_t config = WIFI_INIT_CONFIG_DEFAULT();
    err = esp_wifi_init(&config);
    if (err != ESP_OK) return err;
    s_wifi_initialized = true;
    err = esp_event_handler_instance_register(WIFI_EVENT, ESP_EVENT_ANY_ID,
                                              wifi_event, NULL, &s_wifi_handler);
    if (err != ESP_OK) return err;
    s_wifi_handler_registered = true;
    err = esp_event_handler_instance_register(IP_EVENT, IP_EVENT_STA_GOT_IP,
                                              ip_event, NULL, &s_ip_handler);
    if (err != ESP_OK) return err;
    s_ip_handler_registered = true;
    err = esp_wifi_set_storage(WIFI_STORAGE_FLASH);
    if (err == ESP_OK) err = esp_wifi_set_mode(WIFI_MODE_STA);
    if (err == ESP_OK) {
        s_wifi_stop_needed = true;
        err = esp_wifi_start();
    }
    if (err == ESP_OK) s_wifi_started = true;
    return err;
}

static esp_err_t host_start(void)
{
    esp_err_t err = esp_blufi_register_callbacks(&s_callbacks);
    if (err != ESP_OK) return err;
    err = nimble_port_init();
    if (err != ESP_OK) return err;
    s_host_initialized = true;
    s_port_initialized = true;
    s_host_stopped = xSemaphoreCreateBinary();
    if (!s_host_stopped) return ESP_ERR_NO_MEM;
    s_profile_stopped = xSemaphoreCreateBinary();
    if (!s_profile_stopped) return ESP_ERR_NO_MEM;
    ble_hs_cfg.reset_cb = blufi_reset;
    ble_hs_cfg.sync_cb = blufi_sync;
    ble_hs_cfg.gatts_register_cb = esp_blufi_gatt_svr_register_cb;
    int rc = esp_blufi_gatt_svr_init();
    if (rc != 0) return ESP_FAIL;
    s_gatt_initialized = true;
    rc = ble_svc_gap_device_name_set(DEVICE_NAME);
    if (rc != 0) return ESP_FAIL;
    esp_blufi_btc_init();
    s_btc_initialized = true;
    err = esp_nimble_enable(host_task);
    if (err == ESP_OK) s_host_running = true;
    return err;
}

static esp_err_t stop_failed(esp_err_t error)
{
    s_error = error;
    s_state = MEMO_PROV_FAILED;
    return error;
}

static esp_err_t provisioning_stop(void)
{
    s_accept_callbacks = false;
    s_ble_connected = false;
    if (s_host_initialized) {
        if (s_profile_initialized) esp_blufi_adv_stop();
        if (s_host_running) {
            if (!s_host_stop_requested) {
                int rc = nimble_port_stop();
                if (rc != 0) { ESP_LOGE(TAG, "nimble_port_stop failed: %d", rc); return stop_failed(ESP_FAIL); }
                s_host_stop_requested = true;
            }
            if (xSemaphoreTake(s_host_stopped, pdMS_TO_TICKS(3000)) != pdTRUE) return stop_failed(ESP_ERR_TIMEOUT);
            s_host_running = false;
            s_host_stop_requested = false;
        }
        if (s_gatt_initialized) {
            if (esp_blufi_gatt_svr_deinit() != 0) return stop_failed(ESP_FAIL);
            s_gatt_initialized = false;
        }
        if (s_port_initialized) {
            const esp_err_t err = nimble_port_deinit();
            if (err != ESP_OK) return stop_failed(err);
            s_port_initialized = false;
        }
        if (s_profile_initialized) {
            if (!s_profile_stop_requested) {
                const esp_err_t err = esp_blufi_profile_deinit();
                if (err != ESP_OK) return stop_failed(err);
                s_profile_stop_requested = true;
            }
            if (xSemaphoreTake(s_profile_stopped, pdMS_TO_TICKS(3000)) != pdTRUE)
                return stop_failed(ESP_ERR_TIMEOUT);
            s_profile_stop_requested = false;
            if (s_profile_stop_error != ESP_OK) return stop_failed(s_profile_stop_error);
            s_profile_initialized = false;
        }
        if (s_btc_initialized) {
            esp_blufi_btc_deinit();
            s_btc_initialized = false;
        }
        s_host_initialized = false;
    }
    memo_provision_security_deinit();
    if (s_host_stopped) {
        vSemaphoreDelete(s_host_stopped);
        s_host_stopped = NULL;
    }
    if (s_profile_stopped) {
        vSemaphoreDelete(s_profile_stopped);
        s_profile_stopped = NULL;
    }
    if (s_wifi_stop_needed) {
        // Scan cancellation is best effort; stop/deinit acknowledge ownership.
        esp_wifi_scan_stop();
        const esp_err_t err = esp_wifi_stop();
        if (err == ESP_ERR_WIFI_NOT_INIT) s_wifi_initialized = false;
        else if (err != ESP_OK) return stop_failed(err);
        s_wifi_stop_needed = false;
        s_wifi_started = false;
    }
    if (s_ip_handler_registered) {
        const esp_err_t err = esp_event_handler_instance_unregister(IP_EVENT, IP_EVENT_STA_GOT_IP,
                                                                   s_ip_handler);
        if (err != ESP_OK) return stop_failed(err);
        s_ip_handler_registered = false;
    }
    if (s_wifi_handler_registered) {
        const esp_err_t err = esp_event_handler_instance_unregister(WIFI_EVENT, ESP_EVENT_ANY_ID,
                                                                   s_wifi_handler);
        if (err != ESP_OK) return stop_failed(err);
        s_wifi_handler_registered = false;
    }
    if (s_wifi_initialized) {
        const esp_err_t err = esp_wifi_deinit();
        if (err != ESP_OK && err != ESP_ERR_WIFI_NOT_INIT) return stop_failed(err);
        s_wifi_initialized = false;
    }
    /* netif 归常驻联网所有，这里只放弃引用，不销毁。 */
    s_sta_netif = NULL;
    s_state = MEMO_PROV_IDLE;
    return ESP_OK;
}



/* ---------------- 对外接口 ---------------- */

esp_err_t memo_provision_start(void)
{
    if (s_host_initialized || s_wifi_initialized || s_ip_handler_registered || s_wifi_handler_registered)
        return ESP_ERR_INVALID_STATE;
    s_accept_callbacks = true;
    s_error = ESP_OK;
    s_host_stop_requested = false;
    s_profile_stop_requested = false;
    s_profile_stop_error = ESP_OK;
    memo_network_info_ssid(&s_info, NULL, 0);
    s_wifi_connecting = false;
    s_wifi_got_ip = false;
    s_reconnect_after_disconnect = false;
    s_state = MEMO_PROV_STARTING;

    /* 只启动蓝牙：WiFi 驱动要等手机真的送来凭据再开。
       连接阶段控制器最需要内存，此时不该让 WiFi 的收发缓冲占着。 */
    ESP_LOGI(TAG, "start: BLE only, free heap %u", (unsigned)esp_get_free_heap_size());
    esp_err_t err = host_start();
    ESP_LOGI(TAG, "start: host_start done (%s), free heap %u", esp_err_to_name(err),
             (unsigned)esp_get_free_heap_size());
    if (err != ESP_OK) {
        esp_err_t cleanup = provisioning_stop();
        if (cleanup != ESP_OK) ESP_LOGE(TAG, "provisioning rollback retained ownership: %s", esp_err_to_name(cleanup));
        s_error = err;
        s_state = MEMO_PROV_FAILED;
        ESP_LOGE(TAG, "provisioning start failed: %s", esp_err_to_name(err));
        return err;
    }
    return ESP_OK;
}

esp_err_t memo_provision_stop(void)
{
    return provisioning_stop();
}

memo_provision_state_t memo_provision_state(void)
{
    return s_state;
}

void memo_provision_info(memo_network_info_t *out)
{
    memo_network_info_copy(&s_info, out);
}

esp_err_t memo_provision_error(void)
{
    return s_error;
}

bool memo_provision_has_network(void)
{
    return s_wifi_got_ip;
}
