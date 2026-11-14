// main/memo_text.c —— 纯文本工具，不依赖 ESP-IDF。
#include "memo_text.h"

#include <string.h>

#define ELLIPSIS "\xE2\x80\xA6" /* U+2026 */

static size_t sequence_length(unsigned char lead)
{
    if (lead < 0x80) return 1;
    if ((lead & 0xE0) == 0xC0) return 2;
    if ((lead & 0xF0) == 0xE0) return 3;
    if ((lead & 0xF8) == 0xF0) return 4;
    return 0; /* continuation byte or invalid lead */
}

size_t memo_text_chars(const char *text)
{
    if (text == NULL) return 0;

    size_t count = 0;
    const unsigned char *p = (const unsigned char *)text;
    while (*p != '\0') {
        size_t len = sequence_length(*p);
        if (len == 0) {
            p++; /* stray continuation byte: count it and move on */
            count++;
            continue;
        }
        for (size_t i = 1; i < len; i++) {
            if (p[i] == '\0') { /* truncated sequence at the end */
                len = i;
                break;
            }
        }
        p += len;
        count++;
    }
    return count;
}

size_t memo_text_byte_offset(const char *text, size_t index)
{
    if (text == NULL) return 0;

    const unsigned char *p = (const unsigned char *)text;
    size_t seen = 0;
    while (*p != '\0' && seen < index) {
        size_t len = sequence_length(*p);
        if (len == 0) len = 1;
        for (size_t i = 1; i < len; i++) {
            if (p[i] == '\0') { len = i; break; }
        }
        p += len;
        seen++;
    }
    return (size_t)(p - (const unsigned char *)text);
}

size_t memo_text_prefix(char *dst, size_t cap, const char *text, size_t max_chars)
{
    if (dst == NULL || cap == 0) return 0;

    size_t offset = memo_text_byte_offset(text, max_chars);
    if (offset >= cap) offset = cap - 1;
    /* Never cut in the middle of a sequence when the cap itself is the limit. */
    while (offset > 0 && ((unsigned char)text[offset] & 0xC0) == 0x80 && offset >= cap - 1) {
        offset--;
    }

    memcpy(dst, text, offset);
    dst[offset] = '\0';
    return memo_text_chars(dst);
}

size_t memo_text_ellipsis(char *dst, size_t cap, const char *text, size_t max_chars)
{
    if (dst == NULL || cap == 0) return 0;
    if (text == NULL) {
        dst[0] = '\0';
        return 0;
    }

    size_t total = memo_text_chars(text);
    if (total <= max_chars) {
        return memo_text_prefix(dst, cap, text, total);
    }
    if (max_chars == 0) {
        dst[0] = '\0';
        return 0;
    }

    size_t keep = max_chars - 1; /* one slot for the ellipsis */
    size_t offset = memo_text_byte_offset(text, keep);
    if (offset + sizeof(ELLIPSIS) - 1 >= cap) {
        /* Not enough room even for the ellipsis: truncate plainly. */
        return memo_text_prefix(dst, cap, text, keep);
    }

    memcpy(dst, text, offset);
    memcpy(dst + offset, ELLIPSIS, sizeof(ELLIPSIS) - 1);
    dst[offset + sizeof(ELLIPSIS) - 1] = '\0';
    return keep + 1;
}

bool memo_text_is_valid_utf8(const char *text)
{
    if (text == NULL) return true;
    const unsigned char *p = (const unsigned char *)text;
    while (*p != '\0') {
        if (*p < 0x80) { p++; continue; }
        size_t len;
        if (*p >= 0xC2 && *p <= 0xDF) len = 2;
        else if (*p >= 0xE0 && *p <= 0xEF) len = 3;
        else if (*p >= 0xF0 && *p <= 0xF4) len = 4;
        else return false;
        for (size_t i = 1; i < len; i++) if ((p[i] & 0xC0) != 0x80) return false;
        if ((*p == 0xE0 && p[1] < 0xA0) || (*p == 0xED && p[1] >= 0xA0) ||
            (*p == 0xF0 && p[1] < 0x90) || (*p == 0xF4 && p[1] > 0x8F)) return false;
        p += len;
    }
    return true;
}
