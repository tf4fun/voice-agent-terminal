// main/memo_text.h —— 文本处理的纯逻辑部分：按字符（不是字节）截断与计数。
//
// ASR 输出是任意 UTF-8，列表里那一行必须在字符边界截断，否则会出现半个汉字。
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

/* 返回 UTF-8 字符串里的字符数；非法序列按一个字符计，不越界。 */
size_t memo_text_chars(const char *text);

/* 取第 index 个字符的起始字节偏移；index 超出长度时返回 strlen。 */
size_t memo_text_byte_offset(const char *text, size_t index);

/* 把 text 的前 max_chars 个字符写进 dst（含结尾 NUL）。返回写入的字符数。
   dst 不足时按字符边界截断。 */
size_t memo_text_prefix(char *dst, size_t cap, const char *text, size_t max_chars);

/* 截断并在末尾加省略号「…」：结果最多 max_chars 个字符（含省略号）。
   text 本来就够短时不加省略号。 */
size_t memo_text_ellipsis(char *dst, size_t cap, const char *text, size_t max_chars);

/* UTF-8 合法性：完整且没有过长编码。 */
bool memo_text_is_valid_utf8(const char *text);
