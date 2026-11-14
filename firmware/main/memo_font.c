// main/memo_font.c —— 字体描述符与覆盖率检查。
#include "memo_font.h"

#include "esp_log.h"
#include "memo_text.h"
#include <stdlib.h>
#include <string.h>

static const char *TAG = "memo_font";

static lv_font_t s_body;
static lv_font_t s_label;
static bool s_ready;

void memo_fonts_init(void)
{
    if (s_ready) return;

    /* 复制描述符后挂 fallback：字体数据本身是只读的，这里只扩展查找链。 */
    s_body = memo_font_20;
    s_body.fallback = &s_label;

    s_label = memo_font_16;
    s_label.fallback = NULL;

    s_ready = true;
}

const lv_font_t *memo_font_body(void)
{
    return s_ready ? &s_body : &memo_font_20;
}

const lv_font_t *memo_font_label(void)
{
    return s_ready ? &s_label : &memo_font_16;
}

const lv_font_t *memo_font_meta(void)
{
    return &memo_font_meta_16;
}

bool memo_font_covers(uint32_t codepoint)
{
    lv_font_glyph_dsc_t glyph = {0};
    const lv_font_t *font = memo_font_label();
    if (!lv_font_get_glyph_dsc(font, &glyph, codepoint, 0)) return false;
    return !glyph.is_placeholder;
}

void memo_font_check_text(const char *what, const char *text)
{
    if (text == NULL) return;

    const unsigned char *p = (const unsigned char *)text;
    while (*p != '\0') {
        uint32_t cp;
        int extra;
        if (*p < 0x80) {
            cp = *p;
            extra = 0;
        } else if ((*p & 0xE0) == 0xC0) {
            cp = *p & 0x1F;
            extra = 1;
        } else if ((*p & 0xF0) == 0xE0) {
            cp = *p & 0x0F;
            extra = 2;
        } else if ((*p & 0xF8) == 0xF0) {
            cp = *p & 0x07;
            extra = 3;
        } else {
            p++;
            continue;
        }
        for (int i = 1; i <= extra; i++) {
            if ((p[i] & 0xC0) != 0x80) {
                extra = i - 1;
                break;
            }
            cp = (cp << 6) | (p[i] & 0x3F);
        }
        p += extra + 1;

        if (cp >= 0x20 && !memo_font_covers(cp)) {
            ESP_LOGW(TAG, "%s: missing glyph U+%04X", what, (unsigned)cp);
        }
    }
}

void memo_font_filter_text(char *text, bool body)
{
    if (text == NULL) return;
    const unsigned char *p = (const unsigned char *)text;
    size_t written = 0;
    const lv_font_t *font = body ? memo_font_body() : memo_font_label();
    while (*p) {
        size_t n = 1;
        uint32_t cp = *p;
        bool valid = *p < 0x80;
        if (*p >= 0x80) {
            size_t expected = *p < 0xE0 ? 2 : *p < 0xF0 ? 3 : 4;
            char character[5] = {0};
            size_t copied = 0;
            while (copied < expected && p[copied] != '\0') { character[copied] = p[copied]; copied++; }
            valid = copied == expected && memo_text_is_valid_utf8(character);
            if (valid) {
                n = expected;
                cp = *p & (n == 2 ? 0x1F : n == 3 ? 0x0F : 0x07);
                for (size_t i = 1; i < n; i++) cp = (cp << 6) | (p[i] & 0x3F);
            }
        }
        lv_font_glyph_dsc_t glyph = {0};
        if ((cp == '\n' || cp == '\t') ||
            ((valid || cp < 0x80) && lv_font_get_glyph_dsc(font, &glyph, cp, 0) && !glyph.is_placeholder)) {
            memmove(text + written, p, n); written += n;
        } else text[written++] = '?';
        p += n;
    }
    text[written] = '\0';
}

bool memo_font_label_text(lv_obj_t *label, const char *text, bool body)
{
    if (label == NULL || text == NULL) return false;
    const size_t length = strlen(text);
    char *supported = malloc(length + 1);
    if (supported == NULL) {
        lv_label_set_text_static(label, "读取失败");
        return false;
    }
    memcpy(supported, text, length + 1);
    memo_font_filter_text(supported, body);
    // Reapplying identical filtered text restarts marquee animation.
    if (strcmp(lv_label_get_text(label), supported) != 0) lv_label_set_text(label, supported);
    free(supported);
    return true;
}
