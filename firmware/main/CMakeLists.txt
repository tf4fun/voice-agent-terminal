idf_component_register(
    SRCS "agent_app.c" "agent_client.c" "agent_protocol.c" "agent_capture.c" "agent_ui_status.c"
         "memo_settings.c" "memo_ui_settings.c" "memo_ui_common.c"
         "memo_ui_provision.c" "memo_font.c" "memo_text.c"
         "memo_net.c" "memo_discovery.c" "memo_provision.c" "memo_provision_security.c"
    INCLUDE_DIRS "."
    REQUIRES bsp bt esp_event esp_netif esp_timer esp_wifi json mbedtls mdns
             nvs_flash esp_http_client
)
target_sources(${COMPONENT_LIB} PRIVATE
    "${CMAKE_CURRENT_LIST_DIR}/../assets/fonts/memo_font_16.c"
    "${CMAKE_CURRENT_LIST_DIR}/../assets/fonts/memo_font_20.c"
    "${CMAKE_CURRENT_LIST_DIR}/../assets/fonts/memo_font_meta_16.c"
)
