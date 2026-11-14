// main/memo_font.h —— 中文字体入口。
//
// Noto Sans SC supplies chat, UI and numeric metadata at their required sizes.
// Chat covers both GB2312 Chinese levels. The 20 px UI font falls back to it.
// Numeric metadata uses the same family's 16 px Regular face.
#pragma once

#include "lvgl.h"

LV_FONT_DECLARE(memo_font_16);
LV_FONT_DECLARE(memo_font_20);
LV_FONT_DECLARE(memo_font_meta_16);

/* 建立带 fallback 的可写描述符。必须在创建任何控件之前、持 LVGL 锁调用一次。 */
void memo_fonts_init(void);

/* 20 px Regular UI text, falling back to the expanded 16 px chat font. */
const lv_font_t *memo_font_body(void);

/* 16 px Bold chat and labels, including both GB2312 Chinese levels. */
const lv_font_t *memo_font_label(void);

/* 16 px Regular ASCII for dates and battery percentages. */
const lv_font_t *memo_font_meta(void);

/* 该字体是否覆盖某个码点（用于覆盖率自检，缺字要能被发现而不是画成空白）。 */
bool memo_font_covers(uint32_t codepoint);

/* 逐码点检查并在日志里报告缺字，例如 "missing glyph U+25B6"。
   缺字在屏幕上会变成方框，光看界面很难定位是哪个字符，所以在这里报出来。 */
void memo_font_check_text(const char *what, const char *text);

/* Dynamic text: preserve supported UTF-8, replace unsupported characters with
 * '?'. Static UI literals are separately checked against both font cmaps.
 * Caller holds LVGL lock. Returns false on allocation failure (error label shown). */
bool memo_font_label_text(lv_obj_t *label, const char *text, bool body);

/* Filter an owned UTF-8 display snapshot in place under the LVGL lock. */
void memo_font_filter_text(char *text, bool body);
