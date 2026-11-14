#pragma once

#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"

#define ESP_IPADDR_TYPE_V4 0
#define ESP_IPADDR_TYPE_V6 6

typedef struct {
    uint32_t addr;
} esp_ip4_addr_t;

typedef struct {
    int type;
    union {
        esp_ip4_addr_t ip4;
    } u_addr;
} esp_ip_addr_t;

typedef struct mdns_ip_addr_s {
    esp_ip_addr_t addr;
    struct mdns_ip_addr_s *next;
} mdns_ip_addr_t;

typedef struct {
    const char *key;
    const char *value;
} mdns_txt_item_t;

typedef struct mdns_result_s {
    struct mdns_result_s *next;
    char *instance_name;
    char *hostname;
    uint16_t port;
    mdns_txt_item_t *txt;
    size_t txt_count;
    mdns_ip_addr_t *addr;
} mdns_result_t;

#define IPSTR "%u.%u.%u.%u"
#define IP2STR(ip) \
    (unsigned)((const unsigned char *)&(ip)->addr)[0], \
    (unsigned)((const unsigned char *)&(ip)->addr)[1], \
    (unsigned)((const unsigned char *)&(ip)->addr)[2], \
    (unsigned)((const unsigned char *)&(ip)->addr)[3]

esp_err_t mdns_init(void);
void mdns_free(void);
esp_err_t mdns_query_ptr(const char *service, const char *protocol, uint32_t timeout,
                         size_t max_results, mdns_result_t **results);
esp_err_t mdns_query_srv(const char *instance, const char *service, const char *protocol,
                         uint32_t timeout, mdns_result_t **results);
esp_err_t mdns_query_a(const char *hostname, uint32_t timeout, esp_ip4_addr_t *address);
void mdns_query_results_free(mdns_result_t *results);
