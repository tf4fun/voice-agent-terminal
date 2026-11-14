// main/memo_discovery.c —— mDNS 客户端：找到服务端地址。
#include "memo_discovery.h"

#include <string.h>
#include <stdio.h>
#include <stdatomic.h>
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#include "esp_log.h"
#include "mdns.h"

static const char *TAG = "memo_disc";

static _Atomic memo_discovery_state_t s_state = MEMO_DISCOVERY_IDLE;
static char s_host[64];
static uint16_t s_port;
static bool s_ready;
static SemaphoreHandle_t s_lock, s_address_lock;
static uint32_t s_generation;

typedef struct {
    char host[64], previous_host[64];
    uint16_t port, previous_port;
    uint32_t count;
    bool advance, passed_previous;
} candidate_selection_t;

static esp_err_t init_locked(void)
{
    if (s_ready) return ESP_OK;

    esp_err_t err = mdns_init();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) {
        ESP_LOGE(TAG, "mdns init: %s", esp_err_to_name(err));
        return err;
    }
    s_ready = true;
    return ESP_OK;
}

static bool select_candidate(candidate_selection_t *selection, const char *host, uint16_t port)
{
    if (host == NULL || host[0] == '\0' || port == 0) return false;
    const uint32_t index = selection->count++;
    const bool same = port == selection->previous_port &&
        strncmp(host, selection->previous_host, sizeof(selection->previous_host) - 1) == 0;
    const bool choose = selection->previous_host[0] == '\0' ||
        (!selection->advance && same) ||
        (selection->advance && selection->passed_previous && !same);
    if (index == 0 || choose) {
        snprintf(selection->host, sizeof(selection->host), "%s", host);
        selection->port = port;
    }
    if (same) selection->passed_previous = true;
    return choose;
}

static bool select_direct(candidate_selection_t *selection, const mdns_result_t *result, uint16_t port)
{
    if (port == 0) return false;
    bool has_ipv4 = false;
    for (const mdns_ip_addr_t *address = result->addr; address != NULL; address = address->next) {
        if (address->addr.type != ESP_IPADDR_TYPE_V4) continue;
        has_ipv4 = true;
        char host[16];
        snprintf(host, sizeof(host), IPSTR, IP2STR(&address->addr.u_addr.ip4));
        if (select_candidate(selection, host, port)) return true;
    }
    // TXT is a fallback for results whose address list has no usable IPv4.
    if (has_ipv4) return false;
    for (size_t i = 0; i < result->txt_count; i++) {
        if (result->txt[i].key != NULL && strcmp(result->txt[i].key, "ip") == 0 &&
            select_candidate(selection, result->txt[i].value, port)) return true;
    }
    return false;
}

static bool select_hostname(candidate_selection_t *selection, const char *hostname, uint16_t port)
{
    if (hostname == NULL || hostname[0] == '\0' || port == 0) return false;
    esp_ip4_addr_t address = {0};
    if (mdns_query_a(hostname, 2000, &address) == ESP_OK) {
        char host[16];
        snprintf(host, sizeof(host), IPSTR, IP2STR(&address));
        return select_candidate(selection, host, port);
    }
    // Keep the existing hostname fallback for LWIP's resolver.
    return select_candidate(selection, hostname, port);
}

static bool select_result(candidate_selection_t *selection, const mdns_result_t *result)
{
    const uint32_t before = selection->count;
    if (select_direct(selection, result, result->port)) return true;
    if (selection->count != before) return false;

    // Resolve each PTR instance independently so addresses and ports stay paired.
    if (result->instance_name != NULL && result->instance_name[0] != '\0') {
        mdns_result_t *services = NULL;
        const esp_err_t err = mdns_query_srv(result->instance_name, MEMO_DISCOVERY_SERVICE,
                                             MEMO_DISCOVERY_PROTO, 2000, &services);
        bool selected = false;
        if (err == ESP_OK) {
            for (const mdns_result_t *service = services; service != NULL; service = service->next) {
                const uint16_t port = service->port != 0 ? service->port : result->port;
                const uint32_t service_before = selection->count;
                selected = select_direct(selection, service, port);
                if (!selected && selection->count == service_before)
                    selected = select_direct(selection, result, port);
                if (!selected && selection->count == service_before) {
                    const char *hostname = service->hostname != NULL && service->hostname[0] != '\0' ?
                        service->hostname : result->hostname;
                    selected = select_hostname(selection, hostname, port);
                }
                if (selected) break;
            }
        }
        if (services != NULL) mdns_query_results_free(services);
        if (selected) return true;
        if (selection->count != before) return false;
    }
    return select_hostname(selection, result->hostname, result->port);
}

static esp_err_t search_locked(uint32_t timeout_ms)
{
    if (!s_ready) {
        esp_err_t err = init_locked();
        if (err != ESP_OK) return err;
    }

    candidate_selection_t selection = {0};
    xSemaphoreTake(s_address_lock, portMAX_DELAY);
    snprintf(selection.previous_host, sizeof(selection.previous_host), "%s", s_host);
    selection.previous_port = s_port;
    selection.advance = s_state == MEMO_DISCOVERY_FAILED;
    s_generation++;
    s_state = MEMO_DISCOVERY_SEARCHING;
    xSemaphoreGive(s_address_lock);

    mdns_result_t *results = NULL;
    esp_err_t err = mdns_query_ptr(MEMO_DISCOVERY_SERVICE, MEMO_DISCOVERY_PROTO,
                                   timeout_ms, 4, &results);
    if (err == ESP_OK) {
        for (const mdns_result_t *result = results; result != NULL; result = result->next) {
            if (select_result(&selection, result)) break;
        }
    }
    if (results != NULL) mdns_query_results_free(results);

    if (err != ESP_OK || selection.count == 0) {
        ESP_LOGW(TAG, "no usable %s.%s service found (%s)", MEMO_DISCOVERY_SERVICE,
                 MEMO_DISCOVERY_PROTO, esp_err_to_name(err));
        s_state = MEMO_DISCOVERY_FAILED;
        return ESP_ERR_NOT_FOUND;
    }

    xSemaphoreTake(s_address_lock, portMAX_DELAY);
    snprintf(s_host, sizeof(s_host), "%s", selection.host);
    s_port = selection.port;
    // If the previous endpoint was last or disappeared, selection holds the first.
    s_state = MEMO_DISCOVERY_CANDIDATE;
    xSemaphoreGive(s_address_lock);
    ESP_LOGI(TAG, "server at %s:%u", selection.host, (unsigned)selection.port);
    return ESP_OK;
}

void memo_discovery_stop(void)
{
    if (s_lock == NULL || s_address_lock == NULL) return;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    if (s_ready) mdns_free();
    xSemaphoreTake(s_address_lock, portMAX_DELAY);
    s_generation++;
    s_ready = false;
    s_state = MEMO_DISCOVERY_IDLE;
    s_host[0] = '\0';
    s_port = 0;
    xSemaphoreGive(s_address_lock);
    xSemaphoreGive(s_lock);
    ESP_LOGI(TAG, "released mDNS memory");
}

memo_discovery_state_t memo_discovery_state(void)
{
    return s_state;
}

esp_err_t memo_discovery_init(void)
{
    /* First initialisation is owned by app_main before workers/input start. */
    if (s_lock == NULL) s_lock = xSemaphoreCreateMutex();
    if (s_address_lock == NULL) s_address_lock = xSemaphoreCreateMutex();
    if (s_lock == NULL || s_address_lock == NULL) return ESP_ERR_NO_MEM;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    esp_err_t err = init_locked();
    xSemaphoreGive(s_lock);
    return err;
}

esp_err_t memo_discovery_search(uint32_t timeout_ms)
{
    if (s_lock == NULL || s_address_lock == NULL) return ESP_ERR_INVALID_STATE;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    esp_err_t err = search_locked(timeout_ms);
    xSemaphoreGive(s_lock);
    return err;
}

bool memo_discovery_endpoint(memo_endpoint_t *out)
{
    if (out == NULL || s_address_lock == NULL) return false;
    xSemaphoreTake(s_address_lock, portMAX_DELAY);
    bool valid = s_host[0] != '\0' && s_port != 0 &&
                 (s_state == MEMO_DISCOVERY_CANDIDATE || s_state == MEMO_DISCOVERY_FOUND);
    if (valid) {
        snprintf(out->host, sizeof(out->host), "%s", s_host);
        out->port = s_port; out->generation = s_generation;
        out->confirmed = s_state == MEMO_DISCOVERY_FOUND;
    }
    xSemaphoreGive(s_address_lock);
    return valid;
}

void memo_discovery_confirm(const memo_endpoint_t *endpoint)
{
    if (endpoint == NULL || s_address_lock == NULL) return;
    xSemaphoreTake(s_address_lock, portMAX_DELAY);
    if (endpoint->generation == s_generation && s_state == MEMO_DISCOVERY_CANDIDATE)
        s_state = MEMO_DISCOVERY_FOUND;
    xSemaphoreGive(s_address_lock);
}

void memo_discovery_invalidate(const memo_endpoint_t *endpoint)
{
    if (endpoint == NULL || s_address_lock == NULL) return;
    xSemaphoreTake(s_address_lock, portMAX_DELAY);
    if (endpoint->generation == s_generation &&
        (s_state == MEMO_DISCOVERY_CANDIDATE || s_state == MEMO_DISCOVERY_FOUND)) {
        // Keep only the copied address as a rotation marker. endpoint() hides
        // failed/searching state, and stop() clears the marker on network changes.
        s_generation++;
        s_state = MEMO_DISCOVERY_FAILED;
    }
    xSemaphoreGive(s_address_lock);
}
