#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "agent_capture.h"
#include "agent_chat_layout.h"
#include "agent_client.h"
#include "agent_logic.h"
#include "agent_ui_status.h"
#include "bsp_audio.h"
#include "bsp_battery.h"
#include "bsp_button.h"
#include "bsp_display.h"
#include "bsp_i2c.h"
#include "bsp_pins.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_sleep.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "key_guard.h"
#include "lvgl.h"
#include "memo_browse.h"
#include "memo_discovery.h"
#include "memo_font.h"
#include "memo_key.h"
#include "memo_net.h"
#include "memo_power.h"
#include "memo_provision.h"
#include "memo_settings.h"
#include "memo_ui_common.h"
#include "memo_ui_provision.h"
#include "memo_ui_settings.h"

typedef struct { bsp_btn_t button; bsp_btn_ev_t event; int64_t time_us; } input_event_t;

static const char *TAG = "agent_app";
static QueueHandle_t s_input;
static atomic_bool s_ready, s_release_lost;
static memo_key_t s_keys[BSP_BTN_COUNT];
static key_guard_t s_guard;
static memo_power_t s_power;
static memo_settings_edit_t s_edit;
static agent_page_t s_page = AGENT_PAGE_SESSIONS;
static agent_page_t s_record_return = AGENT_PAGE_SESSIONS;
static agent_view_t s_view;
static uint32_t s_seen_capture;
static int s_selected, s_setting_selected;
static int s_session_offset = -1;
static bool s_sleep_requested, s_manual_sleep;
static bool s_record_requested, s_record_dismissed;
static bool s_wake_ok;
static char s_local_notice[64];
static int64_t s_notice_until;
static int s_backlight = -1, s_soc = -1;
static lv_obj_t *s_transition, *s_screen, *s_status, *s_title;
static lv_obj_t *s_rows[4], *s_row_titles[4], *s_row_details[4];
static bool s_row_selected[4];
static lv_obj_t *s_scroll, *s_text, *s_arc, *s_clock;
static lv_obj_t *s_wave[12];
static uint8_t s_levels[12];
static lv_obj_t *s_chat_card, *s_chat_role, *s_chat_body;
static lv_obj_t *s_chat_wait, *s_chat_wait_arc;
static char s_chat_title[AGENT_TITLE_BYTES + 1], s_chat_status[64];
static char *s_chat_text;
static char s_chat_id[37];
static uint32_t s_chat_generation, s_chat_selection;
static agent_text_kind_t s_chat_kind;
static bool s_chat_wait_animating, s_chat_open_bottom;

static void set_text(lv_obj_t *label, const char *text, bool body)
{
    if (label != NULL && strcmp(lv_label_get_text(label), text) != 0) memo_font_label_text(label, text, body);
}

static lv_obj_t *label(lv_obj_t *parent, int x, int y, int width, bool body, uint32_t color)
{
    lv_obj_t *object = memo_ui_label(parent, body ? memo_font_body() : memo_font_label(), color);
    lv_obj_set_pos(object, x, y);
    lv_obj_set_width(object, width);
    return object;
}

static void arc_rotate(void *object, int32_t angle) { lv_arc_set_rotation(object, angle); }

static void delete_rows(void)
{
    for (int i = 0; i < 4; i++) {
        if (s_rows[i] != NULL) lv_obj_delete(s_rows[i]);
    }
    memset(s_rows, 0, sizeof(s_rows));
    memset(s_row_titles, 0, sizeof(s_row_titles));
    memset(s_row_details, 0, sizeof(s_row_details));
    memset(s_row_selected, 0, sizeof(s_row_selected));
}

static void delete_screen(void)
{
    lv_screen_load(s_transition);
    delete_rows();
    agent_ui_status_destroy();
    if (s_page == AGENT_PAGE_SETTINGS) memo_ui_settings_destroy();
    else if (s_page == AGENT_PAGE_PROVISION) memo_ui_provision_destroy();
    else memo_ui_screen_delete(s_screen);
    s_screen = s_status = s_title = s_scroll = s_text = s_arc = s_clock = NULL;
    memset(s_wave, 0, sizeof(s_wave));
    memset(s_levels, 0, sizeof(s_levels));
    free(s_chat_text);
    s_chat_text = NULL;
    s_chat_card = s_chat_role = s_chat_body = NULL;
    s_chat_wait = s_chat_wait_arc = NULL;
    s_chat_title[0] = s_chat_status[0] = '\0';
    s_chat_id[0] = '\0';
    s_chat_generation = s_chat_selection = UINT32_MAX;
    s_chat_kind = AGENT_TEXT_REPLY;
    s_chat_wait_animating = s_chat_open_bottom = false;
}

static void build_rows(void)
{
    const memo_rows_layout_t layout = memo_rows_layout(BSP_LCD_W, BSP_LCD_H);
    for (int i = 0; i < 4; i++) {
        s_rows[i] = memo_ui_card(s_screen, layout.x, layout.top + i * layout.pitch,
                                 layout.width, layout.row_height, NULL);
        memo_ui_set_selected(s_rows[i], false);
        lv_obj_set_style_border_width(s_rows[i], 0, 0);
        s_row_titles[i] = label(s_rows[i], 12, 0, layout.width - 24, false, UI_FG);
        lv_obj_set_height(s_row_titles[i], memo_font_label()->line_height);
        lv_label_set_long_mode(s_row_titles[i], LV_LABEL_LONG_DOT);
        s_row_details[i] = memo_ui_label(s_rows[i], memo_font_meta(), UI_DIM);
        lv_obj_set_pos(s_row_details[i], 12, 33);
        lv_obj_set_size(s_row_details[i], layout.width - 24, memo_font_meta()->line_height);
        lv_label_set_long_mode(s_row_details[i], LV_LABEL_LONG_DOT);
    }
}

static void build_screen(agent_page_t page)
{
    delete_screen();
    s_page = page;
    if (page == AGENT_PAGE_SETTINGS) {
        memo_ui_settings_build();
        memo_ui_settings_set_selected(s_setting_selected);
        memo_ui_settings_refresh();
        s_screen = memo_ui_settings_screen();
    } else if (page == AGENT_PAGE_PROVISION) {
        memo_ui_provision_build();
        s_screen = memo_ui_provision_screen();
    } else {
        s_screen = lv_obj_create(NULL);
        memo_ui_flat_rect(s_screen, UI_BG, 0);
        const char *title = "会话";
        if (page == AGENT_PAGE_CHAT) title = "对话";
        else if (page == AGENT_PAGE_RECORD) title = "录音";
        else if (page == AGENT_PAGE_PREPARING) title = "准备录音";
        s_title = memo_ui_header(s_screen, title, page != AGENT_PAGE_SESSIONS);
        if (page == AGENT_PAGE_CHAT) {
            const agent_chat_card_layout_t layout = agent_chat_card_layout(
                BSP_LCD_W, BSP_LCD_H, memo_font_label()->line_height);
            s_chat_card = memo_ui_card(s_screen, layout.x, layout.top,
                layout.width, layout.height, NULL);
            lv_obj_set_style_pad_all(s_chat_card, 0, 0);
            s_chat_role = label(s_chat_card, 12, layout.role_y, 40, false, UI_DIM);
            s_scroll = lv_obj_create(s_chat_card);
            memo_ui_flat_rect(s_scroll, UI_SURFACE, 0);
            lv_obj_set_pos(s_scroll, 12, layout.body_y);
            lv_obj_set_size(s_scroll, layout.body_width, layout.body_height);
            lv_obj_set_style_pad_all(s_scroll, 0, 0);
            lv_obj_set_scrollable(s_scroll, true);
            lv_obj_set_scroll_dir(s_scroll, LV_DIR_VER);
            lv_obj_remove_flag(s_scroll, LV_OBJ_FLAG_SCROLL_ELASTIC | LV_OBJ_FLAG_SCROLL_MOMENTUM);
            lv_obj_set_scrollbar_mode(s_scroll, LV_SCROLLBAR_MODE_OFF);
            s_chat_body = label(s_scroll, 0, 0, layout.body_width, false, UI_FG);
            lv_obj_set_height(s_chat_body, LV_SIZE_CONTENT);
            lv_label_set_long_mode(s_chat_body, LV_LABEL_LONG_WRAP);
            lv_obj_set_style_text_line_space(s_chat_body, AGENT_CHAT_LINE_SPACE, 0);
            s_chat_wait = lv_obj_create(s_chat_card);
            memo_ui_flat_rect(s_chat_wait, UI_SURFACE, 0);
            lv_obj_set_pos(s_chat_wait, 12, layout.body_y);
            lv_obj_set_size(s_chat_wait, layout.body_width, layout.body_height);
            s_chat_wait_arc = lv_arc_create(s_chat_wait);
            const int wait_top = (layout.body_height - 52) / 2;
            lv_obj_set_pos(s_chat_wait_arc, (layout.body_width - 52) / 2, wait_top);
            lv_obj_set_size(s_chat_wait_arc, 52, 52);
            lv_arc_set_bg_angles(s_chat_wait_arc, 0, 360);
            lv_arc_set_range(s_chat_wait_arc, 0, 100);
            lv_arc_set_value(s_chat_wait_arc, 24);
            lv_obj_remove_style(s_chat_wait_arc, NULL, LV_PART_KNOB);
            lv_obj_set_style_arc_color(s_chat_wait_arc, lv_color_hex(UI_BORDER), LV_PART_MAIN);
            lv_obj_set_style_arc_color(s_chat_wait_arc, lv_color_hex(UI_FG), LV_PART_INDICATOR);
            lv_obj_set_style_arc_width(s_chat_wait_arc, 2, LV_PART_MAIN);
            lv_obj_set_style_arc_width(s_chat_wait_arc, 2, LV_PART_INDICATOR);
            for (int i = 0; i < 3; i++) {
                lv_obj_t *dot = lv_obj_create(s_chat_wait);
                memo_ui_flat_rect(dot, UI_DIM, 2);
                lv_obj_set_size(dot, 4, 4);
                lv_obj_set_pos(dot, layout.body_width / 2 - 10 + i * 8, wait_top + 24);
            }
            lv_obj_set_hidden(s_chat_wait, true);
            s_status = label(s_chat_card, layout.status_x, layout.status_y, layout.status_width, false, UI_DIM);
            lv_obj_set_style_text_align(s_status, LV_TEXT_ALIGN_RIGHT, 0);
            lv_obj_set_height(s_status, memo_font_label()->line_height);
            lv_label_set_long_mode(s_status, LV_LABEL_LONG_DOT);
            lv_obj_set_hidden(s_status, true);
        } else if (page == AGENT_PAGE_PREPARING) {
            s_arc = lv_arc_create(s_screen);
            lv_obj_set_size(s_arc, 72, 72);
            lv_obj_set_pos(s_arc, 84, 108);
            lv_arc_set_bg_angles(s_arc, 0, 360);
            lv_arc_set_range(s_arc, 0, 100);
            lv_arc_set_value(s_arc, 72);
            lv_obj_remove_style(s_arc, NULL, LV_PART_KNOB);
            lv_obj_set_style_arc_color(s_arc, lv_color_hex(UI_BORDER), LV_PART_MAIN);
            lv_obj_set_style_arc_color(s_arc, lv_color_hex(UI_FG), LV_PART_INDICATOR);
            lv_obj_set_style_arc_width(s_arc, 3, LV_PART_MAIN);
            lv_obj_set_style_arc_width(s_arc, 3, LV_PART_INDICATOR);
            lv_anim_t animation;
            lv_anim_init(&animation);
            lv_anim_set_var(&animation, s_arc);
            lv_anim_set_values(&animation, 0, 360);
            lv_anim_set_duration(&animation, 900);
            lv_anim_set_repeat_count(&animation, LV_ANIM_REPEAT_INFINITE);
            lv_anim_set_exec_cb(&animation, arc_rotate);
            lv_anim_start(&animation);
            s_status = label(s_screen, 24, 208, 192, false, UI_DIM);
            lv_obj_set_style_text_align(s_status, LV_TEXT_ALIGN_CENTER, 0);
            set_text(s_status, "正在连接语音服务", false);
        } else if (page == AGENT_PAGE_RECORD) {
            s_clock = label(s_screen, 24, 54, 192, true, UI_FG);
            lv_obj_set_style_text_align(s_clock, LV_TEXT_ALIGN_CENTER, 0);
            s_status = label(s_screen, 24, 98, 192, false, UI_DIM);
            lv_obj_set_style_text_align(s_status, LV_TEXT_ALIGN_CENTER, 0);
            lv_obj_t *recording = lv_obj_create(s_screen);
            memo_ui_flat_rect(recording, UI_RECORDING, 4);
            lv_obj_set_size(recording, 6, 6);
            lv_obj_set_pos(recording, 68, 111);
            s_scroll = lv_obj_create(s_screen);
            memo_ui_flat_rect(s_scroll, UI_BG, 0);
            lv_obj_set_pos(s_scroll, 24, 200);
            lv_obj_set_size(s_scroll, 192, 88);
            lv_obj_set_style_pad_all(s_scroll, 0, 0);
            lv_obj_set_scrollable(s_scroll, true);
            lv_obj_set_scroll_dir(s_scroll, LV_DIR_VER);
            lv_obj_set_scrollbar_mode(s_scroll, LV_SCROLLBAR_MODE_OFF);
            s_text = label(s_scroll, 0, 0, 192, false, UI_FG);
            lv_label_set_long_mode(s_text, LV_LABEL_LONG_WRAP);
            memo_ui_rule(s_screen, 24, 272, 192);
            for (int i = 0; i < 12; i++) {
                s_wave[i] = lv_obj_create(s_screen);
                memo_ui_flat_rect(s_wave[i], UI_FG, 0);
                lv_obj_set_pos(s_wave[i], 28 + i * 16, 163);
                lv_obj_set_size(s_wave[i], 8, 2);
            }
        }
    }
    lv_screen_load(s_screen);
}

static void select_row(int row, bool selected)
{
    if (s_row_selected[row] == selected) return;
    s_row_selected[row] = selected;
    memo_ui_set_selected(s_rows[row], selected);
    lv_obj_set_style_text_color(s_row_titles[row], lv_color_hex(UI_FG), 0);
    if (s_row_details[row] != NULL)
        lv_obj_set_style_text_color(s_row_details[row], lv_color_hex(UI_DIM), 0);
    const lv_label_long_mode_t mode = selected ? LV_LABEL_LONG_SCROLL_CIRCULAR : LV_LABEL_LONG_DOT;
    if (lv_label_get_long_mode(s_row_titles[row]) != mode) lv_label_set_long_mode(s_row_titles[row], mode);
}

static const char *message_status(const char *state)
{
    if (strcmp(state, "queued") == 0 || strcmp(state, "sending") == 0) return "正在发送";
    if (strcmp(state, "waiting") == 0) return "正在思考";
    if (strcmp(state, "receiving") == 0) return "正在回复";
    if (strcmp(state, "attention") == 0) return "等待审批";
    if (strcmp(state, "unconfirmed") == 0) return "连接中断，可重试";
    if (strcmp(state, "failed") == 0) return "回复失败，可重试";
    if (strcmp(state, "cancelled") == 0) return "已取消";
    if (strcmp(state, "incomplete") == 0) return "回复未完成";
    return "";
}

static void render_sessions(void)
{
    const int count = s_view.session_count;
    if (count == 0) {
        s_selected = 0;
        s_session_offset = s_view.session_offset;
        if (s_rows[0] != NULL) delete_rows();
        agent_ui_status_build(s_screen);
        const agent_status_t status = agent_session_status(memo_net_has_ip(),
            memo_net_state() == MEMO_NET_CONNECTING, s_view.connected, s_view.sessions_ready,
            s_view.sessions_loading, s_view.notice[0] != '\0');
        agent_ui_status_refresh(status, s_view.notice);
        set_text(s_title, "会话", false);
        return;
    }
    agent_ui_status_destroy();
    if (s_rows[0] == NULL) build_rows();
    if (s_session_offset != s_view.session_offset) {
        s_selected = s_view.session_offset < s_session_offset && count > 0 ? count - 1 : 0;
        s_session_offset = s_view.session_offset;
    }
    if (s_selected >= count) s_selected = count > 0 ? count - 1 : 0;
    for (int i = 0; i < 4; i++) {
        lv_obj_set_hidden(s_rows[i], i >= count);
        if (i >= count) continue;
        char detail[80];
        const agent_session_t *session = &s_view.sessions[i];
        set_text(s_row_titles[i], session->title[0] ? session->title : "新会话", false);
        if (session->updated_at > 0) {
            const time_t timestamp = session->updated_at / 1000 + memo_settings_get()->tz_offset_min * 60;
            struct tm date;
            gmtime_r(&timestamp, &date);
            snprintf(detail, sizeof(detail), "%02d-%02d %02d:%02d", date.tm_mon + 1, date.tm_mday, date.tm_hour, date.tm_min);
        } else detail[0] = '\0';
        set_text(s_row_details[i], detail, false);
        select_row(i, i == s_selected);
    }
    set_text(s_title, s_view.notice[0] ? s_view.notice : "会话", false);
    if (lv_label_get_long_mode(s_title) != LV_LABEL_LONG_SCROLL_CIRCULAR)
        lv_label_set_long_mode(s_title, LV_LABEL_LONG_SCROLL_CIRCULAR);
}

static void chat_status(const char *text)
{
    // LVGL's DOT mode edits its displayed buffer, so compare the source text.
    if (strcmp(s_chat_status, text) != 0 && memo_font_label_text(s_status, text, false))
        agent_copy_text(s_chat_status, sizeof(s_chat_status), text);
    lv_obj_set_hidden(s_status, text[0] == '\0');
}

static void chat_wait(bool visible)
{
    lv_obj_set_hidden(s_chat_wait, !visible);
    lv_obj_set_hidden(s_scroll, visible);
    if (visible == s_chat_wait_animating) return;
    s_chat_wait_animating = visible;
    if (!visible) {
        lv_anim_delete(s_chat_wait_arc, arc_rotate);
        return;
    }
    lv_anim_t animation;
    lv_anim_init(&animation);
    lv_anim_set_var(&animation, s_chat_wait_arc);
    lv_anim_set_values(&animation, 0, 360);
    lv_anim_set_duration(&animation, 1600);
    lv_anim_set_repeat_count(&animation, LV_ANIM_REPEAT_INFINITE);
    lv_anim_set_exec_cb(&animation, arc_rotate);
    lv_anim_start(&animation);
}

static void set_background(lv_obj_t *object, uint32_t color)
{
    // Reapplying an unchanged local style still invalidates the whole object.
    if ((lv_color_to_u32(lv_obj_get_style_bg_color(object, 0)) & 0xFFFFFF) != color)
        lv_obj_set_style_bg_color(object, lv_color_hex(color), 0);
}

static void render_chat(void)
{
    const bool input = s_view.text_kind == AGENT_TEXT_INPUT || s_view.text_kind == AGENT_TEXT_DRAFT;
    const bool draft = s_view.text_kind == AGENT_TEXT_DRAFT;
    const bool different = s_chat_selection != s_view.selection || s_chat_kind != s_view.text_kind ||
                           strcmp(s_chat_id, s_view.text_id) != 0;
    const bool loading = s_view.session_switching || s_view.chat_loading || s_view.text_loading;
    const bool generating = !input && agent_message_active(s_view.message.state);
    const bool retain_loading = loading && !generating && s_chat_selection == s_view.selection &&
                                s_chat_text != NULL && s_chat_text[0] != '\0';
    const bool shown_input = retain_loading ?
        (s_chat_kind == AGENT_TEXT_INPUT || s_chat_kind == AGENT_TEXT_DRAFT) : input;
    const uint32_t color = shown_input ? UI_SELECTED : UI_SURFACE;
    const char *title = s_view.title[0] ? s_view.title : "对话";
    if (strcmp(s_chat_title, title) != 0 && memo_font_label_text(s_title, title, false))
        agent_copy_text(s_chat_title, sizeof(s_chat_title), title);
    set_text(s_chat_role, shown_input ? "我" : "助手", false);
    set_background(s_chat_card, color);
    set_background(s_scroll, color);
    set_background(s_chat_wait, color);

    if (generating) {
        chat_wait(true);
        chat_status(s_view.notice[0] ? s_view.notice : message_status(s_view.message.state));
        return;
    }

    if (!loading && !draft && !s_view.turn.present) {
        if (s_chat_text != NULL || strcmp(lv_label_get_text(s_chat_body), "长按确认，开始录音") != 0) {
            lv_label_set_text_static(s_chat_body, "长按确认，开始录音");
            free(s_chat_text);
            s_chat_text = NULL;
        }
        s_chat_selection = s_view.selection;
        s_chat_generation = s_view.text_generation;
        s_chat_kind = s_view.text_kind;
        agent_copy_text(s_chat_id, sizeof(s_chat_id), s_view.text_id);
        set_text(s_chat_role, "对话", false);
        chat_wait(false);
        chat_status(s_view.notice);
        return;
    }

    if (!loading && (different || s_chat_generation != s_view.text_generation)) {
        char *text = agent_client_text_copy(s_view.selection, s_view.text_generation);
        if (text != NULL) {
            char *first = text;
            while (*first == '\n' || *first == '\r') first++;
            if (first != text) memmove(text, first, strlen(first) + 1);
            memo_font_filter_text(text, false);
            const int32_t position = lv_obj_get_scroll_y(s_scroll);
            if (s_chat_text == NULL || strcmp(s_chat_text, text) != 0) {
                lv_label_set_text_static(s_chat_body, text);
                free(s_chat_text);
                s_chat_text = text;
            } else free(text);
            lv_obj_update_layout(s_scroll);
            int32_t target = position;
            if (different) {
                target = s_chat_open_bottom ? position + lv_obj_get_scroll_bottom(s_scroll) : 0;
                s_chat_open_bottom = false;
            }
            if (!input && s_chat_wait_animating) target = 0;
            if (target < 0) target = 0;
            lv_obj_scroll_to_y(s_scroll, target, LV_ANIM_OFF);
            s_chat_selection = s_view.selection;
            s_chat_generation = s_view.text_generation;
            s_chat_kind = s_view.text_kind;
            agent_copy_text(s_chat_id, sizeof(s_chat_id), s_view.text_id);
        }
    }

    const bool current = s_chat_selection == s_view.selection && s_chat_kind == s_view.text_kind &&
                         strcmp(s_chat_id, s_view.text_id) == 0;
    const bool has_text = (current || retain_loading) && s_chat_text != NULL && s_chat_text[0] != '\0';
    const bool reading = (!current && !has_text) || (loading && !has_text);
    chat_wait(reading);
    const char *status = input ? (draft ? "待发送" : "") : message_status(s_view.message.state);
    if (loading || reading) status = "正在读取";
    if (!loading && !has_text && !input) {
        const char *empty = status[0] ? status : "回复为空";
        set_text(s_chat_body, empty, false);
    }
    chat_status(s_view.notice[0] ? s_view.notice : status);
}

static void render(void)
{
    const memo_settings_t *appearance = s_edit.active ? &s_edit.value : memo_settings_get();
    const uint64_t utc = memo_net_now();
    memo_ui_apply_theme(s_screen, memo_theme_dark((memo_theme_t)appearance->theme, utc, appearance->tz_offset_min));
    set_background(s_transition, UI_BG);
    memo_ui_clock_update(utc, appearance->tz_offset_min);
    if (esp_timer_get_time() < s_notice_until)
        agent_copy_text(s_view.notice, sizeof(s_view.notice), s_local_notice);
    if (s_view.capture != AGENT_IDLE && !s_record_dismissed) {
        const agent_page_t page = s_view.capture == AGENT_PREPARING ? AGENT_PAGE_PREPARING : AGENT_PAGE_RECORD;
        if (s_page != page) build_screen(page);
    } else if (s_record_requested) {
        const bool settled = s_view.capture == AGENT_IDLE;
        const agent_page_t next = settled && !s_record_dismissed && s_seen_capture != s_view.capture_serial ?
            AGENT_PAGE_CHAT : s_record_return;
        if (settled && next == AGENT_PAGE_SESSIONS) agent_client_close_session();
        if (s_page != next) build_screen(next);
        if (settled) {
            s_seen_capture = s_view.capture_serial;
            s_record_requested = s_record_dismissed = false;
        }
    }
    if (s_page == AGENT_PAGE_SESSIONS) render_sessions();
    else if (s_page == AGENT_PAGE_CHAT) render_chat();
    else if (s_page == AGENT_PAGE_SETTINGS) memo_ui_settings_refresh_values();
    else if (s_page == AGENT_PAGE_PROVISION) memo_ui_provision_refresh();
    else if (s_page == AGENT_PAGE_RECORD) {
        const uint32_t seconds = agent_capture_duration_ms() / 1000;
        char clock[32];
        snprintf(clock, sizeof(clock), "%02lu:%02lu", (unsigned long)(seconds / 60), (unsigned long)(seconds % 60));
        set_text(s_clock, clock, true);
        set_text(s_status, s_view.capture == AGENT_COMMITTING ? "正在保存" :
            s_view.capture == AGENT_FINALIZING ? "正在收尾" : "正在录音", false);
        set_text(s_text, s_view.text.text, false);
        lv_obj_update_layout(s_scroll);
        lv_obj_scroll_to_y(s_scroll, lv_obj_get_scroll_y(s_scroll) + lv_obj_get_scroll_bottom(s_scroll), LV_ANIM_OFF);
        memmove(s_levels, s_levels + 1, sizeof(s_levels) - 1);
        s_levels[11] = agent_capture_level();
        for (int i = 0; i < 12; i++) {
            const int height = 2 + s_levels[i] * 28 / 100;
            lv_obj_set_pos(s_wave[i], 28 + i * 16, 164 - height / 2);
            lv_obj_set_height(s_wave[i], height);
        }
    }
    memo_ui_battery_update(s_soc);
}

static void ui_notice(const char *text)
{
    agent_copy_text(s_local_notice, sizeof(s_local_notice), text);
    s_notice_until = esp_timer_get_time() + 2000000;
    agent_copy_text(s_view.notice, sizeof(s_view.notice), text);
    if (s_page == AGENT_PAGE_CHAT) chat_status(text);
    else if (s_status != NULL) {
        lv_obj_set_hidden(s_status, false);
        set_text(s_status, text, false);
    }
}

static void show_page(agent_page_t page)
{
    agent_client_copy(&s_view);
    if (bsp_lvgl_lock(1000)) { build_screen(page); render(); bsp_lvgl_unlock(); }
}

static void settings_key(bsp_btn_t button, bool hold)
{
    if (hold) return;
    if (s_edit.active) {
        if (button == BSP_BTN_OK) {
            if (memo_settings_edit_save(&s_edit) == ESP_OK) show_page(AGENT_PAGE_SETTINGS);
            else if (bsp_lvgl_lock(1000)) { memo_ui_settings_error(); bsp_lvgl_unlock(); }
        } else {
            memo_settings_edit_adjust(&s_edit, button == BSP_BTN_UP ? 1 : -1);
            if (s_edit.field == MEMO_EDIT_BRIGHTNESS) bsp_display_backlight(s_edit.value.brightness);
            if (bsp_lvgl_lock(1000)) { memo_ui_settings_show_draft(&s_edit.value); bsp_lvgl_unlock(); }
        }
        return;
    }
    if (button != BSP_BTN_OK) {
        s_setting_selected += button == BSP_BTN_UP ? -1 : 1;
        if (s_setting_selected < 0) s_setting_selected = 0;
        if (s_setting_selected > MEMO_SETTING_ABOUT) s_setting_selected = MEMO_SETTING_ABOUT;
        if (bsp_lvgl_lock(1000)) {
            memo_ui_settings_set_selected(s_setting_selected);
            memo_ui_settings_refresh_values();
            bsp_lvgl_unlock();
        }
        return;
    }
    if (s_setting_selected == MEMO_SETTING_NETWORK) {
        if (agent_client_provision(true) == ESP_OK) show_page(AGENT_PAGE_PROVISION);
    } else if (s_setting_selected == MEMO_SETTING_SERVER) agent_client_refresh();
    else if (s_setting_selected >= MEMO_SETTING_VOLUME && s_setting_selected <= MEMO_SETTING_TIMEZONE) {
        static const memo_settings_field_t fields[] = {MEMO_EDIT_VOLUME, MEMO_EDIT_BRIGHTNESS, MEMO_EDIT_TIMEOUT, MEMO_EDIT_SLEEP, MEMO_EDIT_THEME, MEMO_EDIT_TIMEZONE};
        if (memo_settings_edit_begin(&s_edit, fields[s_setting_selected - MEMO_SETTING_VOLUME]) == ESP_OK && bsp_lvgl_lock(1000)) {
            memo_ui_settings_enter(&s_edit.value);
            bsp_lvgl_unlock();
        }
    }
}

static void exit_page(void)
{
    if (agent_page_is_root(s_page, s_edit.active)) {
        s_manual_sleep = true;
        return;
    }
    const agent_page_t target = agent_page_exit(s_page, s_edit.active);
    if (s_page == AGENT_PAGE_PROVISION) {
        if (agent_client_provision(false) == ESP_OK) show_page(target);
    } else if (s_edit.active) {
        memo_settings_edit_cancel(&s_edit);
        bsp_display_backlight(memo_settings_get()->brightness);
        show_page(target);
    } else if (s_record_requested) {
        agent_client_cancel_record();
        s_record_dismissed = true;
        s_record_return = target;
        show_page(target);
    } else if (s_page == AGENT_PAGE_CHAT) {
        if (agent_client_close_session() == ESP_OK) show_page(target);
    } else if (s_page != target) show_page(target);
}

static void scroll_chat(bsp_btn_t button)
{
    if (!bsp_lvgl_lock(1000)) return;
    const int direction = button == BSP_BTN_UP ? -1 : 1;
    const bool input = s_view.text_kind == AGENT_TEXT_INPUT;
    const bool draft = s_view.text_kind == AGENT_TEXT_DRAFT;
    if (!input && !draft && agent_message_active(s_view.message.state)) {
        if (direction < 0 && !s_view.chat_loading && !s_view.text_loading) {
            s_chat_open_bottom = true;
            agent_client_message_select(true);
        }
        bsp_lvgl_unlock();
        return;
    }
    lv_obj_update_layout(s_scroll);
    const int32_t position = lv_obj_get_scroll_y(s_scroll);
    const int32_t remaining = lv_obj_get_scroll_bottom(s_scroll);
    if (direction < 0 && position > 0) {
        lv_obj_scroll_to_y(s_scroll, position > 46 ? position - 46 : 0, LV_ANIM_OFF);
    } else if (direction > 0 && remaining > 0) {
        const int32_t step = remaining > 46 ? 46 : remaining;
        lv_obj_scroll_to_y(s_scroll, position + step, LV_ANIM_OFF);
    } else if (!draft && !s_view.chat_loading && !s_view.text_loading) {
        if (direction < 0 && !input) {
            s_chat_open_bottom = true;
            agent_client_message_select(true);
        } else if (direction > 0 && input) {
            s_chat_open_bottom = false;
            agent_client_message_select(false);
        } else if (agent_client_history(direction) == ESP_OK) {
            s_chat_open_bottom = direction < 0;
        }
    }
    bsp_lvgl_unlock();
}

static void confirm_chat(void)
{
    if (s_view.busy) return;
    if (s_view.text_kind == AGENT_TEXT_DRAFT) {
        if (agent_client_send(s_view.text.revision, s_view.selection) != ESP_OK && bsp_lvgl_lock(1000)) {
            ui_notice("内容已更新，请稍候");
            bsp_lvgl_unlock();
        }
    } else if (s_view.segment_total > 0) agent_client_review_draft();
    else if (s_view.turn.present &&
             (strcmp(s_view.message.state, "failed") == 0 || strcmp(s_view.message.state, "unconfirmed") == 0)) {
        agent_client_retry(s_view.message.id, s_view.selection);
    }
}

static void gesture(bsp_btn_t button, bool hold)
{
    if (s_sleep_requested) return;
    if (hold && button == BSP_BTN_DOWN) { exit_page(); return; }
    if (hold && button == BSP_BTN_UP && agent_page_is_root(s_page, s_edit.active)) {
        show_page(agent_page_cycle(s_page, agent_root_pages, AGENT_ROOT_PAGE_COUNT));
        return;
    }
    if (s_page == AGENT_PAGE_PROVISION) {
        if (!hold && button == BSP_BTN_OK && agent_client_provision(false) == ESP_OK)
            show_page(AGENT_PAGE_SETTINGS);
        return;
    }
    if (s_page == AGENT_PAGE_SETTINGS) { settings_key(button, hold); return; }
    if (s_view.capture != AGENT_IDLE || s_page == AGENT_PAGE_PREPARING || s_page == AGENT_PAGE_RECORD) return;
    if (hold && button == BSP_BTN_OK && agent_page_can_record(s_page)) {
        s_record_return = s_page;
        s_seen_capture = s_view.capture_serial;
        if (agent_client_record(s_page == AGENT_PAGE_SESSIONS) == ESP_OK) {
            s_record_requested = true;
            s_record_dismissed = false;
            show_page(AGENT_PAGE_PREPARING);
        } else if (bsp_lvgl_lock(1000)) { ui_notice("请先连接服务器"); bsp_lvgl_unlock(); }
        return;
    }
    if (s_page == AGENT_PAGE_SESSIONS) {
        if (hold) return;
        if (button == BSP_BTN_OK) {
            if (s_selected < s_view.session_count && agent_client_open(&s_view.sessions[s_selected]) == ESP_OK)
                show_page(AGENT_PAGE_CHAT);
        } else {
            const agent_step_t step = agent_page_step(s_selected, s_view.session_count, button == BSP_BTN_UP ? -1 : 1,
                s_view.session_offset > 0, s_view.sessions_more);
            s_selected = step.selected;
            if (step.direction != 0) agent_client_sessions_page(step.direction);
        }
    } else if (s_page == AGENT_PAGE_CHAT && !hold) {
        if (button == BSP_BTN_OK) confirm_chat();
        else scroll_chat(button);
    }
}

static void key_callback(bsp_btn_t button, bsp_btn_ev_t event, void *arg)
{
    (void)arg;
    if (event != BSP_BTN_PRESS && event != BSP_BTN_RELEASE && event != BSP_BTN_LONG) return;
    input_event_t input = {.button = button, .event = event, .time_us = esp_timer_get_time()};
    if (s_input != NULL && xQueueSend(s_input, &input, 0) != pdTRUE && event == BSP_BTN_RELEASE) s_release_lost = true;
}

static bool sleep_device(void)
{
    if (bsp_button_read_mv() < BSP_BTN_MV_RELEASED_MIN || !bsp_lvgl_lock(1000)) return false;
    if (esp_deep_sleep_enable_gpio_wakeup(1ULL << BSP_BTN_GPIO, ESP_GPIO_WAKEUP_GPIO_LOW) != ESP_OK) {
        bsp_lvgl_unlock();
        return false;
    }
    int level = 0;
    if (bsp_button_prepare_deep_sleep(&level) != ESP_OK) { esp_restart(); return false; }
    bsp_battery_sleep();
    bsp_audio_sleep();
    bsp_audio_prepare_deep_sleep();
    bsp_i2c_prepare_deep_sleep();
    bsp_display_prepare_deep_sleep();
    esp_deep_sleep_start();
    return false;
}

static void power_tick(int64_t now)
{
    const memo_settings_t *settings = memo_settings_get();
    const bool active = s_view.capture != AGENT_IDLE || (s_view.busy && !s_sleep_requested) || s_edit.active || s_page == AGENT_PAGE_PROVISION ||
        agent_message_active(s_view.message.state) || bsp_button_read_mv() < BSP_BTN_MV_RELEASED_MIN;
    const memo_power_action_t action = s_manual_sleep ? MEMO_POWER_SLEEP : memo_power_poll(&s_power, now / 1000, settings->screen_timeout_s,
                                                      settings->sleep_min, active, s_view.busy && !s_sleep_requested);
    const int brightness = action == MEMO_POWER_ON ? (s_edit.active ? s_edit.value.brightness : settings->brightness) : 0;
    if (brightness != s_backlight) { bsp_display_backlight(brightness); s_backlight = brightness; }
    if (action == MEMO_POWER_SLEEP && !s_sleep_requested) s_sleep_requested = agent_client_pause() == ESP_OK;
    if (s_sleep_requested && action != MEMO_POWER_SLEEP) {
        s_sleep_requested = s_manual_sleep = false;
        agent_client_provision(false);
    } else if (s_sleep_requested && agent_sleep_ready(s_view.paused, s_view.busy,
               bsp_button_read_mv() >= BSP_BTN_MV_RELEASED_MIN) && !sleep_device()) {
        s_sleep_requested = s_manual_sleep = false;
        agent_client_provision(false);
    }
}

static void input_task(void *arg)
{
    (void)arg;
    int64_t last_battery = 0, last_memory = 0;
    agent_capture_state_t last_capture = AGENT_IDLE;
    int last_page = -1;
    for (;;) {
        input_event_t input;
        const bool received = xQueueReceive(s_input, &input, pdMS_TO_TICKS(40)) == pdTRUE;
        if (!s_ready) continue;
        agent_client_copy(&s_view);
        const int64_t now = esp_timer_get_time();
        if (now - last_memory >= 5000000 || s_view.capture != last_capture || (int)s_page != last_page) {
            ESP_LOGI(TAG, "memory page=%d capture=%d free=%u largest=%u minimum=%u input_stack=%u",
                     (int)s_page, (int)s_view.capture,
                     (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                     (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
                     (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT),
                     (unsigned)uxTaskGetStackHighWaterMark(NULL));
            last_memory = now;
            last_capture = s_view.capture;
            last_page = s_page;
        }
        const bool discard_input = atomic_exchange(&s_release_lost, false);
        if (discard_input) {
            xQueueReset(s_input);
            agent_client_release_record();
            memset(s_keys, 0, sizeof(s_keys));
            s_wake_ok = false;
        }
        if (received && !discard_input) {
            const bool release = input.event == BSP_BTN_RELEASE;
            const bool wake = s_power.blank && input.event == BSP_BTN_PRESS && input.button == BSP_BTN_OK;
            const bool consumed = memo_power_key(&s_power, now / 1000, input.event == BSP_BTN_PRESS, release) ||
                                  key_guard_consume(&s_guard, release, now);
            if (wake) s_wake_ok = true;
            memo_key_t *key = &s_keys[input.button];
            if (input.event == BSP_BTN_PRESS) memo_key_press(key, input.time_us, !consumed || s_wake_ok);
            else {
                const memo_key_action_t action = release ? memo_key_release(key, input.time_us, BSP_BTN_LONG_PRESS_MS * 1000) :
                    memo_key_hold(key, input.time_us, BSP_BTN_LONG_PRESS_MS * 1000, true);
                if ((!consumed || (s_wake_ok && !release && action == MEMO_KEY_HOLD)) && action != MEMO_KEY_NONE)
                    gesture(input.button, action == MEMO_KEY_HOLD);
                if (release && input.button == BSP_BTN_OK) {
                    if (s_view.capture == AGENT_PREPARING && s_record_requested) s_record_dismissed = true;
                    agent_client_release_record();
                    s_wake_ok = false;
                }
            }
        }
        for (int button = 0; button < BSP_BTN_COUNT; button++) {
            if (memo_key_hold(&s_keys[button], now, BSP_BTN_LONG_PRESS_MS * 1000, false) == MEMO_KEY_HOLD)
                gesture(button, true);
        }
        if (now - last_battery >= 5000000) { s_soc = bsp_battery_soc(); last_battery = now; }
        if (bsp_lvgl_lock(1000)) { render(); bsp_lvgl_unlock(); }
        power_tick(now);
    }
}

void app_main(void)
{
    ESP_LOGI(TAG, "voice agent terminal starting");
    if (memo_settings_init() != ESP_OK || bsp_i2c_init() != ESP_OK || bsp_display_init() != ESP_OK || bsp_lvgl_init() == NULL) return;
    bsp_display_backlight(memo_settings_get()->brightness);
    if (bsp_audio_init() != ESP_OK) ESP_LOGW(TAG, "audio initialization failed");
    bsp_audio_set_volume(memo_settings_get()->volume);
    bsp_battery_init();
    if (memo_net_start() != ESP_OK || memo_discovery_init() != ESP_OK) ESP_LOGW(TAG, "network initialization failed");
    if (agent_capture_init() != ESP_OK || agent_client_init() != ESP_OK) { ESP_LOGE(TAG, "worker allocation failed"); return; }
    s_input = xQueueCreate(16, sizeof(input_event_t));
    if (s_input == NULL || xTaskCreate(input_task, "agent_input", 6144, NULL, 5, NULL) != pdPASS) return;
    if (esp_sleep_get_wakeup_cause() == ESP_SLEEP_WAKEUP_GPIO) key_guard_arm(&s_guard, esp_timer_get_time(), 3000000);
    if (bsp_button_init(key_callback, NULL) != ESP_OK) { ESP_LOGE(TAG, "button initialization failed"); return; }
    key_guard_sync_released(&s_guard, bsp_button_read_mv() >= BSP_BTN_MV_RELEASED_MIN);
    if (bsp_lvgl_lock(1000)) {
        memo_fonts_init();
        s_transition = lv_obj_create(NULL);
        memo_ui_flat_rect(s_transition, UI_BG, 0);
        build_screen(AGENT_PAGE_SESSIONS);
        render();
        bsp_lvgl_unlock();
    }
    s_power.activity_ms = esp_timer_get_time() / 1000;
    s_ready = true;
    ESP_LOGI(TAG, "ready; heap %u, largest %u", (unsigned)esp_get_free_heap_size(),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT));
}

