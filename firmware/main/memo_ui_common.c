// main/memo_ui_common.c
#include "memo_ui_common.h"
#include "memo_browse.h"
#include "memo_font.h"
#include "memo_progress.h"
#include "memo_theme.h"
#include <stdio.h>
#include <string.h>

// Background, foreground, secondary, border, surface, selected, recording,
// accent, accent tint. Values stay unique per palette for live recoloring.
// The light palette stays warm and never uses pure white, which glares on the
// small backlit panel.
static const uint32_t s_palettes[2][UI_COLOR_COUNT] = {
    {0xECE6DC, 0x26231E, 0x716A5F, 0xD8CFBF, 0xF6F1E8, 0xE3DBCD, 0xC8463D, 0x5A4AD0, 0xE4DDEC},
    {0x0B0C10, 0xF2F3F5, 0x8A91A0, 0x2A2E38, 0x17191F, 0x21242C, 0xFF5964, 0x8F82FF, 0x24203D},
};
static bool s_dark = true;
static lv_obj_t *s_header_clock;
static uint64_t s_clock_utc;
static int s_clock_offset;

uint32_t memo_ui_color(memo_ui_color_t color)
{
    return s_palettes[s_dark][color];
}

static void recolor(lv_obj_t *object, const uint32_t *previous, const uint32_t *next)
{
    static const lv_style_prop_t properties[] = {
        LV_STYLE_BG_COLOR, LV_STYLE_TEXT_COLOR, LV_STYLE_BORDER_COLOR,
        LV_STYLE_ARC_COLOR, LV_STYLE_LINE_COLOR, LV_STYLE_OUTLINE_COLOR,
    };
    static const lv_part_t parts[] = {LV_PART_MAIN, LV_PART_INDICATOR, LV_PART_KNOB};
    for (unsigned part = 0; part < sizeof(parts) / sizeof(parts[0]); part++) {
        for (unsigned property = 0; property < sizeof(properties) / sizeof(properties[0]); property++) {
            lv_style_value_t value;
            if (lv_obj_get_local_style_prop(object, properties[property], &value, parts[part]) != LV_STYLE_RES_FOUND)
                continue;
            const uint32_t color = lv_color_to_u32(value.color) & 0xFFFFFF;
            for (int role = 0; role < UI_COLOR_COUNT; role++) {
                if (color == previous[role]) {
                    value.color = lv_color_hex(next[role]);
                    lv_obj_set_local_style_prop(object, properties[property], value, parts[part]);
                    break;
                }
            }
        }
    }
    for (uint32_t child = 0; child < lv_obj_get_child_count(object); child++)
        recolor(lv_obj_get_child(object, child), previous, next);
}

void memo_ui_apply_theme(lv_obj_t *screen, bool dark)
{
    if (s_dark == dark) return;
    const uint32_t *previous = s_palettes[s_dark];
    s_dark = dark;
    if (screen != NULL) {
        recolor(screen, previous, s_palettes[s_dark]);
        lv_obj_invalidate(screen);
    }
}

void memo_ui_clock_update(uint64_t utc_seconds, int offset_minutes)
{
    s_clock_utc = utc_seconds;
    s_clock_offset = offset_minutes;
    if (s_header_clock == NULL) return;
    char text[8] = "--:--";
    if (utc_seconds != 0) {
        const int minutes = memo_local_minutes(utc_seconds, offset_minutes);
        snprintf(text, sizeof(text), "%02d:%02d", minutes / 60, minutes % 60);
    }
    if (strcmp(lv_label_get_text(s_header_clock), text) != 0)
        lv_label_set_text(s_header_clock, text);
}

void memo_ui_flat_rect(lv_obj_t *obj, uint32_t color, int radius)
{
    lv_obj_remove_style_all(obj);
    lv_obj_set_style_bg_color(obj, lv_color_hex(color), 0);
    lv_obj_set_style_bg_opa(obj, LV_OPA_COVER, 0);
    lv_obj_set_style_radius(obj, radius, 0);
    lv_obj_set_scrollable(obj, false);
}

lv_obj_t *memo_ui_card(lv_obj_t *parent, int x, int y, int w, int h, lv_obj_t **shadow_out)
{
    if (shadow_out != NULL) *shadow_out = NULL;

    lv_obj_t *face = lv_obj_create(parent);
    memo_ui_flat_rect(face, UI_SURFACE, VM_RADIUS_CARD);
    lv_obj_set_style_border_color(face, lv_color_hex(UI_BORDER), 0);
    lv_obj_set_style_border_width(face, 0, 0);
    lv_obj_set_pos(face, x, y);
    lv_obj_set_size(face, w, h);
    return face;
}

void memo_ui_rule(lv_obj_t *screen, int x, int y, int w)
{
    lv_obj_t *rule = lv_obj_create(screen);
    memo_ui_flat_rect(rule, UI_BORDER, 0);
    lv_obj_set_size(rule, w, 1);
    lv_obj_set_pos(rule, x, y);
}

lv_obj_t *memo_ui_meter_create(lv_obj_t *parent, int width, int height, lv_obj_t **fill)
{
    lv_obj_t *track = lv_obj_create(parent);
    memo_ui_flat_rect(track, UI_BORDER, height / 2);
    lv_obj_set_style_pad_all(track, 0, 0);
    lv_obj_set_size(track, width, height);
    const memo_progress_layout_t layout = memo_progress_layout(width, height);
    *fill = lv_obj_create(track);
    memo_ui_flat_rect(*fill, UI_ACCENT, height / 2);
    lv_obj_set_pos(*fill, layout.x, layout.y);
    lv_obj_set_size(*fill, 0, layout.height);
    return track;
}

void memo_ui_set_selected(lv_obj_t *box, bool selected)
{
    if (box == NULL) return;
    // An inset outline keeps child positions identical to unselected rows.
    lv_obj_set_style_bg_color(box, lv_color_hex(selected ? UI_ACCENT_SOFT : UI_SURFACE), 0);
    lv_obj_set_style_outline_color(box, lv_color_hex(UI_ACCENT), 0);
    lv_obj_set_style_outline_pad(box, -2, 0);
    lv_obj_set_style_outline_width(box, selected ? 2 : 0, 0);
}

void memo_ui_page_dots(lv_obj_t *screen, int active, int count)
{
    const int dot = 6, pill = 16, gap = 5;
    const int total = pill + (count - 1) * (dot + gap);
    int x = (lv_display_get_horizontal_resolution(NULL) - total) / 2;
    for (int i = 0; i < count; i++) {
        lv_obj_t *mark = lv_obj_create(screen);
        memo_ui_flat_rect(mark, i == active ? UI_ACCENT : UI_BORDER, dot / 2);
        lv_obj_set_size(mark, i == active ? pill : dot, dot);
        lv_obj_set_pos(mark, x, 31);
        x += (i == active ? pill : dot) + gap;
    }
}

lv_obj_t *memo_ui_icon_badge(lv_obj_t *parent, int x, int y, int size)
{
    lv_obj_t *badge = lv_obj_create(parent);
    memo_ui_flat_rect(badge, UI_ACCENT_SOFT, LV_RADIUS_CIRCLE);
    lv_obj_set_pos(badge, x, y);
    lv_obj_set_size(badge, size, size);
    return badge;
}

lv_obj_t *memo_ui_spinner(lv_obj_t *parent, int size, int width)
{
    lv_obj_t *arc = lv_arc_create(parent);
    lv_obj_set_size(arc, size, size);
    lv_arc_set_bg_angles(arc, 0, 360);
    lv_arc_set_range(arc, 0, 100);
    lv_arc_set_value(arc, 28);
    lv_obj_remove_style(arc, NULL, LV_PART_KNOB);
    lv_obj_set_style_arc_color(arc, lv_color_hex(UI_BORDER), LV_PART_MAIN);
    lv_obj_set_style_arc_color(arc, lv_color_hex(UI_ACCENT), LV_PART_INDICATOR);
    lv_obj_set_style_arc_width(arc, width, LV_PART_MAIN);
    lv_obj_set_style_arc_width(arc, width, LV_PART_INDICATOR);
    lv_obj_set_style_arc_rounded(arc, true, LV_PART_INDICATOR);
    return arc;
}

lv_obj_t *memo_ui_label(lv_obj_t *parent, const lv_font_t *font, uint32_t color)
{
    lv_obj_t *label = lv_label_create(parent);
    lv_obj_set_style_text_font(label, font, 0);
    lv_obj_set_style_text_color(label, lv_color_hex(color), 0);
    return label;
}

void memo_ui_card_visible(lv_obj_t *card, lv_obj_t *shadow, bool visible)
{
    lv_obj_t *objects[] = {card, shadow};
    for (size_t i = 0; i < sizeof(objects) / sizeof(objects[0]); i++) {
        if (objects[i] == NULL) continue;
        lv_obj_set_hidden(objects[i], !visible);
    }
}

lv_obj_t *memo_ui_scrollbar_create(lv_obj_t *screen, int x, int y, int h)
{
    lv_obj_t *track = lv_obj_create(screen);
    memo_ui_flat_rect(track, UI_BORDER, 2);
    lv_obj_set_pos(track, x, y);
    lv_obj_set_size(track, 4, h);

    lv_obj_t *thumb = lv_obj_create(track);
    memo_ui_flat_rect(thumb, UI_ACCENT, 2);
    lv_obj_set_pos(thumb, 0, 0);
    lv_obj_set_size(thumb, 4, 8);
    lv_obj_set_hidden(track, true);
    return track;
}

void memo_ui_scrollbar_update(lv_obj_t *track, int track_h, int total, int visible, int top)
{
    if (track == NULL) return;
    const memo_scrollbar_t thumb = memo_scrollbar_thumb(total, visible, top, track_h, 12);
    lv_obj_set_hidden(track, !thumb.visible);
    if (!thumb.visible) return;
    lv_obj_t *child = lv_obj_get_child(track, 0);
    if (child == NULL) return;
    lv_obj_set_pos(child, 0, thumb.offset);
    lv_obj_set_size(child, 4, thumb.length);
}

static lv_obj_t *s_batteries[6];
static int s_battery_count;
static int s_soc = -1;

void memo_ui_screen_delete(lv_obj_t *screen)
{
    if (screen == NULL) return;
    if (s_header_clock != NULL && lv_obj_get_parent(s_header_clock) == screen) s_header_clock = NULL;
    for (int i = 0; i < s_battery_count;) {
        if (lv_obj_get_parent(s_batteries[i]) == screen) {
            s_batteries[i] = s_batteries[--s_battery_count];
            s_batteries[s_battery_count] = NULL;
        } else i++;
    }
    lv_obj_delete(screen);
}

void memo_ui_battery_update(int soc)
{
    s_soc = soc;
    char text[12] = "--";
    if (soc >= 0) snprintf(text, sizeof(text), "%d%%", soc > 100 ? 100 : soc);
    const uint32_t color = soc >= 0 && soc <= 15 ? UI_RECORDING : UI_DIM;
    for (int i = 0; i < s_battery_count; i++) {
        if (strcmp(lv_label_get_text(s_batteries[i]), text) != 0) lv_label_set_text(s_batteries[i], text);
        if ((lv_color_to_u32(lv_obj_get_style_text_color(s_batteries[i], 0)) & 0xFFFFFF) != color)
            lv_obj_set_style_text_color(s_batteries[i], lv_color_hex(color), 0);
    }
}

lv_obj_t *memo_ui_battery(lv_obj_t *screen)
{
    const memo_header_layout_t layout = memo_header_layout(
        lv_display_get_horizontal_resolution(NULL), false);
    lv_obj_t *label = memo_ui_label(screen, memo_font_meta(), UI_DIM);
    lv_obj_set_pos(label, layout.battery_x, 8);
    lv_obj_set_size(label, 48, memo_font_meta()->line_height);
    lv_label_set_long_mode(label, LV_LABEL_LONG_CLIP);
    lv_obj_set_style_text_align(label, LV_TEXT_ALIGN_RIGHT, 0);
    if (s_battery_count < (int)(sizeof(s_batteries)/sizeof(s_batteries[0])))
        s_batteries[s_battery_count++] = label;
    memo_ui_battery_update(s_soc);
    return label;
}

lv_obj_t *memo_ui_header(lv_obj_t *screen, const char *text, bool back)
{
    const memo_header_layout_t layout = memo_header_layout(
        lv_display_get_horizontal_resolution(NULL), back);
    lv_obj_t *title = memo_ui_label(screen, memo_font_label(), UI_FG);
    lv_label_set_text(title, text);
    lv_obj_set_pos(title, layout.title_x, 2);
    lv_obj_set_size(title, layout.title_width, memo_font_label()->line_height);
    lv_obj_set_style_text_align(title, LV_TEXT_ALIGN_CENTER, 0);
    lv_label_set_long_mode(title, LV_LABEL_LONG_DOT);
    memo_ui_battery(screen);
    s_header_clock = memo_ui_label(screen, memo_font_meta(), UI_DIM);
    lv_obj_set_pos(s_header_clock, layout.clock_x, 8);
    lv_obj_set_size(s_header_clock, layout.clock_width, memo_font_meta()->line_height);
    memo_ui_clock_update(s_clock_utc, s_clock_offset);
    if (back) {
        static const lv_point_precise_t points[] = {{6, 0}, {0, 6}, {6, 12}};
        lv_obj_t *icon = lv_line_create(screen);
        lv_line_set_points(icon, points, 3);
        lv_obj_set_pos(icon, 16, 12);
        lv_obj_set_style_line_color(icon, lv_color_hex(UI_FG), 0);
        lv_obj_set_style_line_width(icon, 2, 0);
        lv_obj_set_style_line_rounded(icon, true, 0);
    }
    return title;
}
