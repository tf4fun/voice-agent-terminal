#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "freertos/semphr.h"
#include "mdns.h"
#include "memo_discovery.h"

struct test_semaphore { unsigned depth; };
static struct test_semaphore s_semaphores[2];
static size_t s_semaphore_count, s_allocations;
static const mdns_result_t *s_ptr_result;
static esp_err_t s_init_error, s_ptr_error;
static unsigned s_init_calls, s_free_calls, s_srv_calls, s_a_calls;

typedef struct {
    const char *instance;
    const mdns_result_t *results;
    esp_err_t error;
} srv_fixture_t;

typedef struct {
    const char *hostname;
    esp_ip4_addr_t address;
    esp_err_t error;
} a_fixture_t;

static srv_fixture_t s_srv[4];
static a_fixture_t s_a[4];
static size_t s_srv_count, s_a_count;

SemaphoreHandle_t xSemaphoreCreateMutex(void)
{
    assert(s_semaphore_count < 2);
    return &s_semaphores[s_semaphore_count++];
}

void xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t timeout)
{
    assert(semaphore != NULL && semaphore->depth == 0 && timeout == UINT32_MAX);
    semaphore->depth++;
}

void xSemaphoreGive(SemaphoreHandle_t semaphore)
{
    assert(semaphore != NULL && semaphore->depth == 1);
    semaphore->depth--;
}

static void *allocate(size_t size)
{
    void *memory = calloc(1, size);
    assert(memory != NULL);
    s_allocations++;
    return memory;
}

static void release(void *memory)
{
    if (memory == NULL) return;
    assert(s_allocations > 0);
    s_allocations--;
    free(memory);
}

static char *copy_text(const char *text)
{
    if (text == NULL) return NULL;
    char *copy = allocate(strlen(text) + 1);
    memcpy(copy, text, strlen(text) + 1);
    return copy;
}

static mdns_result_t *copy_results(const mdns_result_t *source)
{
    mdns_result_t *head = NULL;
    mdns_result_t **tail = &head;
    for (; source != NULL; source = source->next) {
        mdns_result_t *copy = allocate(sizeof(*copy));
        *tail = copy;
        tail = &copy->next;
        copy->instance_name = copy_text(source->instance_name);
        copy->hostname = copy_text(source->hostname);
        copy->port = source->port;
        copy->txt_count = source->txt_count;
        if (source->txt_count > 0) {
            copy->txt = allocate(source->txt_count * sizeof(*copy->txt));
            for (size_t i = 0; i < source->txt_count; i++) {
                copy->txt[i].key = copy_text(source->txt[i].key);
                copy->txt[i].value = copy_text(source->txt[i].value);
            }
        }
        mdns_ip_addr_t **address_tail = &copy->addr;
        for (const mdns_ip_addr_t *address = source->addr; address != NULL; address = address->next) {
            mdns_ip_addr_t *address_copy = allocate(sizeof(*address_copy));
            address_copy->addr = address->addr;
            *address_tail = address_copy;
            address_tail = &address_copy->next;
        }
    }
    return head;
}

void mdns_query_results_free(mdns_result_t *results)
{
    s_free_calls++;
    while (results != NULL) {
        mdns_result_t *next = results->next;
        release(results->instance_name);
        release(results->hostname);
        for (size_t i = 0; i < results->txt_count; i++) {
            release((void *)results->txt[i].key);
            release((void *)results->txt[i].value);
        }
        release(results->txt);
        while (results->addr != NULL) {
            mdns_ip_addr_t *next_address = results->addr->next;
            release(results->addr);
            results->addr = next_address;
        }
        release(results);
        results = next;
    }
}

esp_err_t mdns_init(void)
{
    s_init_calls++;
    return s_init_error;
}

void mdns_free(void) {}

esp_err_t mdns_query_ptr(const char *service, const char *protocol, uint32_t timeout,
                         size_t max_results, mdns_result_t **results)
{
    assert(strcmp(service, "_voiceagent") == 0 && strcmp(protocol, "_tcp") == 0);
    assert(timeout == 50 && max_results == 4);
    assert(s_semaphores[0].depth == 1 && s_semaphores[1].depth == 0);
    *results = copy_results(s_ptr_result);
    return s_ptr_error;
}

esp_err_t mdns_query_srv(const char *instance, const char *service, const char *protocol,
                         uint32_t timeout, mdns_result_t **results)
{
    assert(strcmp(service, "_voiceagent") == 0 && strcmp(protocol, "_tcp") == 0);
    assert(timeout == 2000 && s_semaphores[1].depth == 0);
    s_srv_calls++;
    *results = NULL;
    for (size_t i = 0; i < s_srv_count; i++) {
        if (strcmp(instance, s_srv[i].instance) == 0) {
            *results = copy_results(s_srv[i].results);
            return s_srv[i].error;
        }
    }
    return ESP_ERR_NOT_FOUND;
}

esp_err_t mdns_query_a(const char *hostname, uint32_t timeout, esp_ip4_addr_t *address)
{
    assert(timeout == 2000 && s_semaphores[1].depth == 0);
    s_a_calls++;
    for (size_t i = 0; i < s_a_count; i++) {
        if (strcmp(hostname, s_a[i].hostname) == 0) {
            *address = s_a[i].address;
            return s_a[i].error;
        }
    }
    return ESP_ERR_NOT_FOUND;
}

static esp_ip4_addr_t ip4(unsigned a, unsigned b, unsigned c, unsigned d)
{
    esp_ip4_addr_t address;
    unsigned char *bytes = (unsigned char *)&address.addr;
    bytes[0] = a; bytes[1] = b; bytes[2] = c; bytes[3] = d;
    return address;
}

static mdns_ip_addr_t address4(unsigned last)
{
    return (mdns_ip_addr_t){.addr = {.type = ESP_IPADDR_TYPE_V4, .u_addr.ip4 = ip4(10, 0, 0, last)}};
}

static void reset_fixture(void)
{
    memo_discovery_stop();
    assert(s_allocations == 0);
    assert(memo_discovery_state() == MEMO_DISCOVERY_IDLE);
    memo_endpoint_t endpoint;
    assert(!memo_discovery_endpoint(&endpoint));
    s_ptr_result = NULL;
    s_init_error = s_ptr_error = ESP_OK;
    s_srv_count = s_a_count = 0;
    s_free_calls = s_srv_calls = s_a_calls = 0;
    assert(memo_discovery_init() == ESP_OK);
}

static memo_endpoint_t expect_candidate(const char *host, uint16_t port)
{
    assert(memo_discovery_search(50) == ESP_OK);
    memo_endpoint_t endpoint;
    assert(memo_discovery_endpoint(&endpoint));
    assert(strcmp(endpoint.host, host) == 0 && endpoint.port == port);
    assert(!endpoint.confirmed && memo_discovery_state() == MEMO_DISCOVERY_CANDIDATE);
    assert(s_allocations == 0);
    return endpoint;
}

static void test_rotation_and_generations(void)
{
    reset_fixture();
    mdns_ip_addr_t first_ip = address4(1), second_ip = address4(2);
    mdns_result_t second = {.instance_name = "compatible-v0.2", .port = 8082, .addr = &second_ip};
    mdns_result_t first = {.instance_name = "old-v0.1", .port = 8081, .addr = &first_ip, .next = &second};
    s_ptr_result = &first;
    memo_endpoint_t old = expect_candidate("10.0.0.1", 8081);
    memo_discovery_invalidate(&old);
    memo_endpoint_t current = expect_candidate("10.0.0.2", 8082);
    memo_discovery_invalidate(&old);
    memo_discovery_confirm(&old);
    memo_endpoint_t unchanged;
    assert(memo_discovery_endpoint(&unchanged));
    assert(unchanged.generation == current.generation && !unchanged.confirmed);
    memo_discovery_confirm(&current);
    assert(memo_discovery_endpoint(&unchanged) && unchanged.confirmed);
    memo_discovery_invalidate(&current);
    for (int i = 0; i < 6; i++) {
        current = expect_candidate(i % 2 == 0 ? "10.0.0.1" : "10.0.0.2", i % 2 == 0 ? 8081 : 8082);
        memo_discovery_invalidate(&old);
        assert(memo_discovery_endpoint(&unchanged) && unchanged.generation == current.generation);
        memo_discovery_invalidate(&current);
    }
    assert(s_free_calls == 8 && s_srv_calls == 0 && s_a_calls == 0);
}

static void test_reordered_candidates(void)
{
    reset_fixture();
    mdns_ip_addr_t ip_a = address4(11), ip_b = address4(12), ip_c = address4(13);
    mdns_result_t c = {.port = 8013, .addr = &ip_c};
    mdns_result_t b = {.port = 8012, .addr = &ip_b, .next = &c};
    mdns_result_t a = {.port = 8011, .addr = &ip_a, .next = &b};
    s_ptr_result = &a;
    memo_endpoint_t endpoint = expect_candidate("10.0.0.11", 8011);
    memo_discovery_invalidate(&endpoint);
    // Replies arrive in C,A,B order. Advance by identity, not the former index.
    c.next = &a; a.next = &b; b.next = NULL; s_ptr_result = &c;
    endpoint = expect_candidate("10.0.0.12", 8012);
    memo_discovery_invalidate(&endpoint);
    c.next = NULL; a.next = &b; b.next = &c; s_ptr_result = &a;
    endpoint = expect_candidate("10.0.0.13", 8013);
    memo_discovery_invalidate(&endpoint);
    endpoint = expect_candidate("10.0.0.11", 8011);
    memo_discovery_confirm(&endpoint);
    // A confirmed endpoint remains selected even when another result arrives first.
    c.next = &a; a.next = &b; b.next = NULL; s_ptr_result = &c;
    endpoint = expect_candidate("10.0.0.11", 8011);
    memo_discovery_invalidate(&endpoint);
    // If that endpoint disappears, restart at the first current candidate.
    c.next = &b; s_ptr_result = &c;
    expect_candidate("10.0.0.13", 8013);
}

static void test_same_host_different_ports(void)
{
    reset_fixture();
    mdns_ip_addr_t address = address4(14);
    mdns_result_t second = {.port = 8115, .addr = &address};
    mdns_result_t first = {.port = 8114, .addr = &address, .next = &second};
    s_ptr_result = &first;
    memo_endpoint_t endpoint = expect_candidate("10.0.0.14", 8114);
    memo_discovery_invalidate(&endpoint);
    endpoint = expect_candidate("10.0.0.14", 8115);
    memo_discovery_invalidate(&endpoint);
    expect_candidate("10.0.0.14", 8114);
}

static void test_multiple_addresses_and_reset(void)
{
    reset_fixture();
    mdns_ip_addr_t last = address4(4), first_ip = address4(3);
    first_ip.next = &last;
    mdns_ip_addr_t ipv6 = {.addr.type = ESP_IPADDR_TYPE_V6, .next = &first_ip};
    mdns_txt_item_t txt = {.key = "ip", .value = "10.0.0.99"};
    mdns_result_t result = {.port = 8080, .addr = &ipv6, .txt = &txt, .txt_count = 1};
    s_ptr_result = &result;
    memo_endpoint_t old = expect_candidate("10.0.0.3", 8080);
    memo_discovery_invalidate(&old);
    memo_endpoint_t next = expect_candidate("10.0.0.4", 8080);
    memo_discovery_invalidate(&next);
    next = expect_candidate("10.0.0.3", 8080);
    memo_discovery_invalidate(&next);
    memo_discovery_stop();
    assert(memo_discovery_init() == ESP_OK);
    next = expect_candidate("10.0.0.3", 8080);
    memo_discovery_invalidate(&old);
    memo_discovery_confirm(&old);
    memo_endpoint_t snapshot;
    assert(memo_discovery_endpoint(&snapshot) && snapshot.generation == next.generation && !snapshot.confirmed);
    assert(s_srv_calls == 0 && s_a_calls == 0);
}

static void test_txt_srv_and_a_fallbacks(void)
{
    reset_fixture();
    mdns_txt_item_t txt = {.key = "ip", .value = "10.0.0.5"};
    mdns_result_t second = {.instance_name = "instance-two"};
    mdns_result_t first = {.port = 8085, .txt = &txt, .txt_count = 1, .next = &second};
    mdns_result_t service = {.hostname = "host-two", .port = 8086};
    s_ptr_result = &first;
    s_srv[s_srv_count++] = (srv_fixture_t){"instance-two", &service, ESP_OK};
    s_a[s_a_count++] = (a_fixture_t){"host-two", ip4(10, 0, 0, 6), ESP_OK};
    memo_endpoint_t endpoint = expect_candidate("10.0.0.5", 8085);
    memo_discovery_invalidate(&endpoint);
    endpoint = expect_candidate("10.0.0.6", 8086);
    assert(s_srv_calls == 1 && s_a_calls == 1 && s_free_calls == 3);
    memo_discovery_invalidate(&endpoint);
    expect_candidate("10.0.0.5", 8085);
    assert(s_free_calls == 5); // Wrapping traverses and frees the second instance too.
}

static void test_srv_preserves_ptr_addresses(void)
{
    reset_fixture();
    mdns_ip_addr_t first_ip = address4(7), second_ip = address4(8);
    first_ip.next = &second_ip;
    mdns_result_t result = {.instance_name = "missing-port", .addr = &first_ip};
    mdns_result_t service = {.port = 8087};
    s_ptr_result = &result;
    s_srv[s_srv_count++] = (srv_fixture_t){"missing-port", &service, ESP_OK};
    memo_endpoint_t endpoint = expect_candidate("10.0.0.7", 8087);
    memo_discovery_invalidate(&endpoint);
    expect_candidate("10.0.0.8", 8087);
    assert(s_srv_calls == 2 && s_free_calls == 4 && s_a_calls == 0);
}

static void test_failed_queries_and_changed_results(void)
{
    reset_fixture();
    mdns_ip_addr_t first_ip = address4(9), second_ip = address4(10);
    mdns_result_t second = {.port = 8090, .addr = &second_ip};
    mdns_result_t first = {.port = 8089, .addr = &first_ip, .next = &second};
    s_ptr_result = &first;
    s_ptr_error = ESP_FAIL;
    assert(memo_discovery_search(50) == ESP_ERR_NOT_FOUND && s_allocations == 0);
    assert(s_free_calls == 1);
    memo_endpoint_t endpoint;
    assert(!memo_discovery_endpoint(&endpoint));
    s_ptr_error = ESP_OK;
    endpoint = expect_candidate("10.0.0.9", 8089);
    memo_discovery_invalidate(&endpoint);
    endpoint = expect_candidate("10.0.0.10", 8090);
    memo_discovery_invalidate(&endpoint);
    s_ptr_result = &second;
    expect_candidate("10.0.0.10", 8090); // A shorter result set wraps to its first item.
    memo_discovery_stop();
    s_init_error = ESP_FAIL;
    assert(memo_discovery_init() == ESP_FAIL);
    assert(memo_discovery_search(50) == ESP_FAIL);
    assert(!memo_discovery_endpoint(&endpoint));
    s_init_error = ESP_OK;
    expect_candidate("10.0.0.10", 8090);
    memo_discovery_stop();
    s_init_error = ESP_ERR_INVALID_STATE;
    assert(memo_discovery_init() == ESP_OK);
    expect_candidate("10.0.0.10", 8090);
}

static void test_failed_srv_and_hostname_fallback(void)
{
    reset_fixture();
    mdns_result_t result = {.instance_name = "partial", .hostname = "unresolved.local", .port = 8091};
    mdns_result_t partial_service = {.hostname = "ignored-on-error", .port = 8092};
    s_ptr_result = &result;
    s_srv[s_srv_count++] = (srv_fixture_t){"partial", &partial_service, ESP_FAIL};
    expect_candidate("unresolved.local", 8091);
    assert(s_srv_calls == 1 && s_a_calls == 1 && s_free_calls == 2 && s_allocations == 0);
    reset_fixture();
    mdns_ip_addr_t ipv6 = {.addr.type = ESP_IPADDR_TYPE_V6};
    mdns_result_t unusable = {.addr = &ipv6};
    s_ptr_result = &unusable;
    memo_endpoint_t endpoint;
    assert(memo_discovery_search(50) == ESP_ERR_NOT_FOUND && !memo_discovery_endpoint(&endpoint));
    assert(s_allocations == 0);
}

int main(void)
{
    assert(memo_discovery_search(50) == ESP_ERR_INVALID_STATE);
    assert(!memo_discovery_endpoint(NULL));
    test_rotation_and_generations();
    test_reordered_candidates();
    test_same_host_different_ports();
    test_multiple_addresses_and_reset();
    test_txt_srv_and_a_fallbacks();
    test_srv_preserves_ptr_addresses();
    test_failed_queries_and_changed_results();
    test_failed_srv_and_hostname_fallback();
    memo_discovery_stop();
    assert(s_allocations == 0 && s_init_calls > 0);
    assert(s_semaphores[0].depth == 0 && s_semaphores[1].depth == 0);
    puts("PASS: mDNS candidate rotation, multiple IPv4 addresses, TXT/SRV/A fallbacks, cleanup and stale generations");
    return 0;
}
