#pragma once

#include <stdbool.h>
#include <stdint.h>

void memo_provision_security_negotiate(uint8_t *data, int len, uint8_t **output_data,
                          int *output_len, bool *need_free);
int memo_provision_security_encrypt(uint8_t iv8, uint8_t *data, int len);
int memo_provision_security_decrypt(uint8_t iv8, uint8_t *data, int len);
uint16_t memo_provision_security_checksum(uint8_t iv8, uint8_t *data, int len);
int memo_provision_security_init(void);
void memo_provision_security_deinit(void);
