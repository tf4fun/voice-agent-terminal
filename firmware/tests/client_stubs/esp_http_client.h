#pragma once
#include <stdbool.h>
#include <stdint.h>
#include "esp_err.h"
typedef struct fake_http *esp_http_client_handle_t;
typedef enum { HTTP_METHOD_GET, HTTP_METHOD_POST, HTTP_METHOD_PATCH, HTTP_METHOD_DELETE } esp_http_client_method_t;
#define ESP_ERR_HTTP_EAGAIN 0x7007
#define HTTP_EVENT_ON_HEADER 1
typedef struct {
    int event_id;
    void *user_data;
    char *header_key, *header_value;
} esp_http_client_event_t;
typedef struct {
    const char *url;
    esp_http_client_method_t method;
    int timeout_ms, buffer_size, buffer_size_tx;
    bool disable_auto_redirect, keep_alive_enable;
    esp_err_t (*event_handler)(esp_http_client_event_t *event);
    void *user_data;
} esp_http_client_config_t;
esp_http_client_handle_t esp_http_client_init(const esp_http_client_config_t *config);
esp_err_t esp_http_client_set_header(esp_http_client_handle_t client, const char *key, const char *value);
esp_err_t esp_http_client_open(esp_http_client_handle_t client, int size);
int esp_http_client_write(esp_http_client_handle_t client, const char *data, int size);
int64_t esp_http_client_fetch_headers(esp_http_client_handle_t client);
int esp_http_client_read(esp_http_client_handle_t client, char *data, int size);
bool esp_http_client_is_complete_data_received(esp_http_client_handle_t client);
int esp_http_client_get_status_code(esp_http_client_handle_t client);
esp_err_t esp_http_client_set_timeout_ms(esp_http_client_handle_t client, int timeout);
esp_err_t esp_http_client_close(esp_http_client_handle_t client);
esp_err_t esp_http_client_cleanup(esp_http_client_handle_t client);
