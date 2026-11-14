// Bounded, copied network display data shared by Wi-Fi/BLE and the UI.
#pragma once

typedef struct {
    char ssid[33];
    char ip[16];
} memo_network_info_t;
