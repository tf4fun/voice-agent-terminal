// Short critical sections for bounded network display snapshots.
#pragma once
#include <string.h>
#include "freertos/FreeRTOS.h"
#include "memo_network_info.h"

typedef struct {
    portMUX_TYPE lock;
    memo_network_info_t value;
} memo_network_info_store_t;

#define MEMO_NETWORK_INFO_STORE_INIT { .lock = portMUX_INITIALIZER_UNLOCKED }

static inline void memo_network_info_ssid(memo_network_info_store_t *store,
                                          const unsigned char *ssid, size_t len)
{
    if (len > sizeof(store->value.ssid) - 1) len = sizeof(store->value.ssid) - 1;
    portENTER_CRITICAL(&store->lock);
    memset(store->value.ssid, 0, sizeof(store->value.ssid));
    if (ssid != NULL) memcpy(store->value.ssid, ssid, len);
    /* Changing credentials invalidates any address of the previous network. */
    store->value.ip[0] = '\0';
    portEXIT_CRITICAL(&store->lock);
}

static inline void memo_network_info_ip(memo_network_info_store_t *store, const char *ip)
{
    portENTER_CRITICAL(&store->lock);
    size_t len = ip == NULL ? 0 : strnlen(ip, sizeof(store->value.ip) - 1);
    memset(store->value.ip, 0, sizeof(store->value.ip));
    if (len != 0) memcpy(store->value.ip, ip, len);
    portEXIT_CRITICAL(&store->lock);
}

static inline void memo_network_info_copy(memo_network_info_store_t *store,
                                          memo_network_info_t *out)
{
    if (out == NULL) return;
    portENTER_CRITICAL(&store->lock);
    *out = store->value;
    portEXIT_CRITICAL(&store->lock);
}
