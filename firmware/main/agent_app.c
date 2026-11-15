#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "agent_capture.h"
#include "agent_chat_layout.h"
#include "agent_client.h"
#include "agent_trace.h"
#include "agent_logic.h"
#include "agent_speech.h"
#include "agent_voice.h"
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
static lv_obj_t *s_row_badges[4], *s_row_initials[4];
static bool s_row_selected[4];
static lv_obj_t *s_scroll, *s_text, *s_arc, *s_clock, *s_record_dot;
#define AGENT_WAVE_BARS 3
#define AGENT_WAVE_WIDTH 10
#define AGENT_WAVE_PITCH 18
#define AGENT_WAVE_CENTER 166
static lv_obj_t *s_wave[AGENT_WAVE_BARS];
static uint8_t s_levels[AGENT_WAVE_BARS];
static lv_obj_t *s_chat_card, *s_chat_role, *s_chat_body, *s_chat_dot;
static lv_obj_t *s_chat_wait, *s_chat_wait_arc;
static char s_chat_title[AGENT_TITLE_BYTES + 1], s_chat_status[64];
static char *s_chat_text;
static char s_chat_id[37];
static uint32_t s_chat_generation, s_chat_selection;
static agent_text_kind_t s_chat_kind;
static bool s_chat_wait_animating, s_chat_open_bottom;

// Voice page (see agent_voice.h for the layout grid).
static lv_obj_t *s_voice_orb, *s_voice_arc, *s_voice_status, *s_voice_timer, *s_voice_side, *s_voice_side_sub;
static lv_obj_t *s_voice_strip, *s_voice_strip_text, *s_voice_strip_spin, *s_voice_strip_tag;
static lv_obj_t *s_voice_panel, *s_voice_panel_count, *s_voice_panel_scroll, *s_voice_panel_body, *s_voice_hints;
static int s_voice_mode = -1, s_voice_hint_set = -1, s_voice_review = -1, s_voice_sentences;
static bool s_voice_small, s_voice_arc_spinning, s_voice_spin_spinning;
static char *s_voice_reply, *s_voice_draft, *s_voice_question;
static char s_voice_reply_key[96], s_voice_question_id[37];
static uint32_t s_voice_draft_generation = UINT32_MAX, s_voice_draft_selection;
static uint16_t s_voice_starts[AGENT_VOICE_MAX_SENTENCES], s_voice_ends[AGENT_VOICE_MAX_SENTENCES];
// Line review: the reply without Markdown marks, wrapped to the strip's width.
#define VOICE_MAX_LINES 256
static char *s_voice_display;
static uint16_t s_voice_lines[VOICE_MAX_LINES + 1];
static int s_voice_line_count;
static int64_t s_voice_review_at; // Last review key; review ends after 15 s without one.
// At rest the strip shows the last reply one line at a time, advancing on its
// own (a vertical ticker); the first key press holds the line in view.
static const char *s_ticker_source;
// The sentence being spoken, shown one line at a time at about the speech rate.
enum { SPEAK_RUNE_US = 260000 }; // ~3.8 characters per second, measured on the reader voice.
static char s_speak_text[AGENT_SPEECH_SENTENCE_BYTES];
static uint16_t s_speak_index;
static int64_t s_speak_at;
static int s_speak_line;
static int s_ticker_line = -1;
static int64_t s_ticker_next;
static char s_think_id[37];
static int64_t s_think_since;
static agent_speech_view_t s_speech;
// Spoken-reply follower: the newest exchange seen in this session.
static char s_play_session[37], s_play_seen[37];
static bool s_play_baseline;

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
    memset(s_row_badges, 0, sizeof(s_row_badges));
    memset(s_row_initials, 0, sizeof(s_row_initials));
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
    s_screen = s_status = s_title = s_scroll = s_text = s_arc = s_clock = s_record_dot = NULL;
    memset(s_wave, 0, sizeof(s_wave));
    memset(s_levels, 0, sizeof(s_levels));
    free(s_chat_text);
    s_chat_text = NULL;
    s_chat_card = s_chat_role = s_chat_body = s_chat_dot = NULL;
    s_chat_wait = s_chat_wait_arc = NULL;
    s_chat_title[0] = s_chat_status[0] = '\0';
    s_chat_id[0] = '\0';
    s_chat_generation = s_chat_selection = UINT32_MAX;
    s_chat_kind = AGENT_TEXT_REPLY;
    s_chat_wait_animating = s_chat_open_bottom = false;
    s_voice_orb = s_voice_arc = s_voice_status = s_voice_timer = s_voice_side = s_voice_side_sub = NULL;
    s_voice_strip = s_voice_strip_text = s_voice_strip_spin = s_voice_strip_tag = NULL;
    s_voice_panel = s_voice_panel_count = s_voice_panel_scroll = s_voice_panel_body = s_voice_hints = NULL;
    s_ticker_source = NULL;
    s_ticker_line = -1;
    s_speak_text[0] = '\0';
    s_voice_review = -1; // Line offsets belong to the reply being left.
    s_voice_mode = s_voice_hint_set = -1;
    s_voice_small = s_voice_arc_spinning = s_voice_spin_spinning = false;
    free(s_voice_reply);
    free(s_voice_draft);
    free(s_voice_question);
    free(s_voice_display);
    s_voice_reply = s_voice_draft = s_voice_question = s_voice_display = NULL;
    s_voice_line_count = 0;
    s_voice_reply_key[0] = s_voice_question_id[0] = '\0';
    s_voice_draft_generation = UINT32_MAX;
    s_voice_sentences = 0;
}

static void build_rows(void)
{
    const memo_rows_layout_t layout = memo_rows_layout(BSP_LCD_W, BSP_LCD_H);
    const int badge = 36, text_x = 12 + badge + 10;
    for (int i = 0; i < 4; i++) {
        s_rows[i] = memo_ui_card(s_screen, layout.x, layout.top + i * layout.pitch,
                                 layout.width, layout.row_height, NULL);
        memo_ui_set_selected(s_rows[i], false);
        s_row_badges[i] = memo_ui_icon_badge(s_rows[i], 12, (layout.row_height - badge) / 2, badge);
        s_row_initials[i] = memo_ui_label(s_row_badges[i], memo_font_label(), UI_ACCENT);
        lv_obj_set_size(s_row_initials[i], badge, memo_font_label()->line_height);
        lv_obj_set_style_text_align(s_row_initials[i], LV_TEXT_ALIGN_CENTER, 0);
        lv_obj_align(s_row_initials[i], LV_ALIGN_CENTER, 0, 1);
        s_row_titles[i] = label(s_rows[i], text_x, 2, layout.width - text_x - 12, false, UI_FG);
        lv_obj_set_height(s_row_titles[i], memo_font_label()->line_height);
        lv_label_set_long_mode(s_row_titles[i], LV_LABEL_LONG_DOT);
        s_row_details[i] = memo_ui_label(s_rows[i], memo_font_meta(), UI_DIM);
        lv_obj_set_pos(s_row_details[i], text_x, 33);
        lv_obj_set_size(s_row_details[i], layout.width - text_x - 12, memo_font_meta()->line_height);
        lv_label_set_long_mode(s_row_details[i], LV_LABEL_LONG_DOT);
    }
}

// First UTF-8 character of a title, used as the session row's initial.
static void title_initial(const char *title, char *out, size_t capacity)
{
    size_t length = 1;
    const unsigned char lead = (unsigned char)title[0];
    if (lead >= 0xF0) length = 4;
    else if (lead >= 0xE0) length = 3;
    else if (lead >= 0xC0) length = 2;
    for (size_t i = 1; i < length; i++)
        if (((unsigned char)title[i] & 0xC0) != 0x80) { length = 1; break; }
    if (lead == 0 || length + 1 > capacity) length = 0;
    memcpy(out, title, length);
    out[length] = '\0';
    if (lead >= 'a' && lead <= 'z' && length == 1) out[0] = (char)(lead - 'a' + 'A');
}


static lv_obj_t *bare(lv_obj_t *parent)
{
    lv_obj_t *object = lv_obj_create(parent);
    lv_obj_remove_style_all(object);
    lv_obj_set_scrollable(object, false);
    lv_obj_set_clickable(object, false);
    return object;
}

static void spin(lv_obj_t *arc, bool on, bool *running, uint32_t period)
{
    if (on == *running) return;
    *running = on;
    lv_obj_set_hidden(arc, !on);
    if (!on) { lv_anim_delete(arc, arc_rotate); return; }
    lv_anim_t animation;
    lv_anim_init(&animation);
    lv_anim_set_var(&animation, arc);
    lv_anim_set_values(&animation, 0, 360);
    lv_anim_set_duration(&animation, period);
    lv_anim_set_repeat_count(&animation, LV_ANIM_REPEAT_INFINITE);
    lv_anim_set_exec_cb(&animation, arc_rotate);
    lv_anim_start(&animation);
}

static void build_voice(void)
{
    // A thin comet arc that orbits the orb while Hermes thinks.
    s_voice_arc = lv_arc_create(s_screen);
    lv_obj_set_size(s_voice_arc, VOICE_ORB + 18, VOICE_ORB + 18);
    lv_obj_set_pos(s_voice_arc, VOICE_ORB_X - 9, VOICE_ORB_Y - 9);
    lv_arc_set_bg_angles(s_voice_arc, 0, 360);
    lv_arc_set_angles(s_voice_arc, 0, 80);
    lv_obj_remove_style(s_voice_arc, NULL, LV_PART_KNOB);
    lv_obj_set_clickable(s_voice_arc, false);
    lv_obj_set_style_arc_opa(s_voice_arc, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_arc_width(s_voice_arc, 4, LV_PART_INDICATOR);
    lv_obj_set_style_arc_color(s_voice_arc, lv_color_hex(UI_ACCENT), LV_PART_INDICATOR);
    lv_obj_set_style_arc_rounded(s_voice_arc, true, LV_PART_INDICATOR);
    lv_obj_set_hidden(s_voice_arc, true);

    s_voice_orb = memo_ui_icon_badge(s_screen, VOICE_ORB_X, VOICE_ORB_Y, VOICE_ORB);
    lv_obj_set_style_outline_pad(s_voice_orb, 0, 0);
    lv_obj_set_style_outline_opa(s_voice_orb, LV_OPA_20, 0);
    for (int i = 0; i < AGENT_WAVE_BARS; i++) {
        s_wave[i] = lv_obj_create(s_voice_orb);
        memo_ui_flat_rect(s_wave[i], UI_ACCENT, 5);
    }
    s_voice_status = label(s_screen, 16, VOICE_STATUS_Y, BSP_LCD_W - 32, false, UI_FG);
    lv_obj_set_style_text_align(s_voice_status, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_set_height(s_voice_status, memo_font_label()->line_height);
    lv_label_set_long_mode(s_voice_status, LV_LABEL_LONG_DOT);
    s_voice_timer = memo_ui_label(s_screen, &lv_font_montserrat_40, UI_FG);
    lv_obj_set_pos(s_voice_timer, 0, VOICE_TIMER_Y);
    lv_obj_set_width(s_voice_timer, BSP_LCD_W);
    lv_obj_set_style_text_align(s_voice_timer, LV_TEXT_ALIGN_CENTER, 0);
    lv_obj_set_hidden(s_voice_timer, true);
    s_voice_side = label(s_screen, VOICE_SIDE_X, VOICE_SIDE_Y, BSP_LCD_W - VOICE_SIDE_X - 18, false, UI_FG);
    s_voice_side_sub = label(s_screen, VOICE_SIDE_X, VOICE_SIDE_Y + 24, BSP_LCD_W - VOICE_SIDE_X - 18, false, UI_DIM);
    lv_obj_set_hidden(s_voice_side, true);
    lv_obj_set_hidden(s_voice_side_sub, true);

    // The one-line strip; it expands into the draft panel.
    s_voice_strip = memo_ui_card(s_screen, VOICE_STRIP_X, VOICE_STRIP_Y, VOICE_STRIP_W, VOICE_STRIP_H, NULL);
    lv_obj_set_style_radius(s_voice_strip, 14, 0);
    lv_obj_set_style_border_width(s_voice_strip, 1, 0);
    lv_obj_set_style_pad_all(s_voice_strip, 0, 0);
    s_voice_strip_spin = memo_ui_spinner(s_voice_strip, 18, 3);
    lv_obj_set_pos(s_voice_strip_spin, 12, (VOICE_STRIP_H - 18) / 2 - 1);
    lv_obj_set_hidden(s_voice_strip_spin, true);
    s_voice_strip_text = label(s_voice_strip, 14, (VOICE_STRIP_H - memo_font_label()->line_height) / 2 - 1, VOICE_STRIP_W - 28, false, UI_FG);
    lv_obj_set_height(s_voice_strip_text, memo_font_label()->line_height);
    lv_label_set_long_mode(s_voice_strip_text, LV_LABEL_LONG_DOT);
    s_voice_strip_tag = memo_ui_label(s_voice_strip, memo_font_meta(), UI_DIM);
    lv_obj_set_pos(s_voice_strip_tag, VOICE_STRIP_W - 48, (VOICE_STRIP_H - memo_font_meta()->line_height) / 2 - 1);
    lv_obj_set_width(s_voice_strip_tag, 36);
    lv_obj_set_style_text_align(s_voice_strip_tag, LV_TEXT_ALIGN_RIGHT, 0);
    lv_obj_set_hidden(s_voice_strip_tag, true);
    lv_obj_set_hidden(s_voice_strip, true);

    s_voice_panel = memo_ui_card(s_screen, VOICE_STRIP_X, VOICE_PANEL_Y, VOICE_STRIP_W, VOICE_PANEL_H, NULL);
    lv_obj_set_style_radius(s_voice_panel, 14, 0);
    lv_obj_set_style_border_width(s_voice_panel, 1, 0);
    lv_obj_set_style_pad_all(s_voice_panel, 0, 0);
    lv_obj_t *head = label(s_voice_panel, 14, 4, 100, false, UI_DIM);
    lv_label_set_text_static(head, "我说的");
    s_voice_panel_count = memo_ui_label(s_voice_panel, memo_font_label(), UI_DIM);
    lv_obj_set_pos(s_voice_panel_count, VOICE_STRIP_W - 104, 4);
    lv_obj_set_width(s_voice_panel_count, 88);
    lv_obj_set_style_text_align(s_voice_panel_count, LV_TEXT_ALIGN_RIGHT, 0);
    s_voice_panel_scroll = lv_obj_create(s_voice_panel);
    memo_ui_flat_rect(s_voice_panel_scroll, UI_SURFACE, 0);
    lv_obj_set_pos(s_voice_panel_scroll, 14, 32);
    lv_obj_set_size(s_voice_panel_scroll, VOICE_STRIP_W - 24, VOICE_PANEL_H - 40);
    lv_obj_set_style_pad_all(s_voice_panel_scroll, 0, 0);
    lv_obj_set_style_pad_right(s_voice_panel_scroll, 8, 0);
    lv_obj_set_scroll_dir(s_voice_panel_scroll, LV_DIR_VER);
    lv_obj_remove_flag(s_voice_panel_scroll, LV_OBJ_FLAG_SCROLL_ELASTIC | LV_OBJ_FLAG_SCROLL_MOMENTUM);
    lv_obj_set_scrollbar_mode(s_voice_panel_scroll, LV_SCROLLBAR_MODE_AUTO);
    lv_obj_set_style_width(s_voice_panel_scroll, 4, LV_PART_SCROLLBAR);
    lv_obj_set_style_radius(s_voice_panel_scroll, 2, LV_PART_SCROLLBAR);
    lv_obj_set_style_bg_color(s_voice_panel_scroll, lv_color_hex(UI_ACCENT), LV_PART_SCROLLBAR);
    lv_obj_set_style_bg_opa(s_voice_panel_scroll, LV_OPA_COVER, LV_PART_SCROLLBAR);
    s_voice_panel_body = label(s_voice_panel_scroll, 0, 0, VOICE_STRIP_W - 36, false, UI_FG);
    lv_obj_set_height(s_voice_panel_body, LV_SIZE_CONTENT);
    lv_label_set_long_mode(s_voice_panel_body, LV_LABEL_LONG_WRAP);
    lv_obj_set_style_text_line_space(s_voice_panel_body, AGENT_CHAT_LINE_SPACE, 0);
    lv_obj_set_hidden(s_voice_panel, true);

    s_voice_hints = bare(s_screen);
    lv_obj_set_pos(s_voice_hints, 0, VOICE_HINTS_Y);
    lv_obj_set_size(s_voice_hints, BSP_LCD_W, VOICE_HINTS_H);
    lv_obj_set_flex_flow(s_voice_hints, LV_FLEX_FLOW_ROW);
    lv_obj_set_flex_align(s_voice_hints, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_column(s_voice_hints, 14, 0);
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
        if (page == AGENT_PAGE_CHAT || page == AGENT_PAGE_VOICE) title = s_view.title[0] ? s_view.title : "对话";
        s_title = memo_ui_header(s_screen, title, page != AGENT_PAGE_SESSIONS);
        if (page == AGENT_PAGE_SESSIONS) memo_ui_page_dots(s_screen, 0, AGENT_ROOT_PAGE_COUNT);
        // Two marks in a session: voice (left) and the text card (right).
        if (page == AGENT_PAGE_VOICE || page == AGENT_PAGE_CHAT) memo_ui_page_dots(s_screen, page == AGENT_PAGE_CHAT, 2);
        if (page == AGENT_PAGE_CHAT) {
            const agent_chat_card_layout_t layout = agent_chat_card_layout(
                BSP_LCD_W, BSP_LCD_H, memo_font_label()->line_height);
            s_chat_card = memo_ui_card(s_screen, layout.x, layout.top,
                layout.width, layout.height, NULL);
            lv_obj_set_style_radius(s_chat_card, VM_RADIUS_PANEL, 0);
            lv_obj_set_style_pad_all(s_chat_card, 0, 0);
            s_chat_dot = lv_obj_create(s_chat_card);
            memo_ui_flat_rect(s_chat_dot, UI_ACCENT, 4);
            lv_obj_set_size(s_chat_dot, 8, 8);
            lv_obj_set_pos(s_chat_dot, 14, layout.role_y + 15);
            s_chat_role = label(s_chat_card, 28, layout.role_y + 2, 40, false, UI_DIM);
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
            const int wait_size = 44;
            s_chat_wait_arc = memo_ui_spinner(s_chat_wait, wait_size, 4);
            lv_obj_set_pos(s_chat_wait_arc, (layout.body_width - wait_size) / 2, (layout.body_height - wait_size) / 2 - 12);
            lv_obj_set_hidden(s_chat_wait, true);
            s_status = label(s_chat_card, layout.status_x, layout.status_y + 2, layout.status_width, false, UI_DIM);
            lv_obj_set_style_text_align(s_status, LV_TEXT_ALIGN_RIGHT, 0);
            lv_obj_set_height(s_status, memo_font_label()->line_height);
            lv_label_set_long_mode(s_status, LV_LABEL_LONG_DOT);
            lv_obj_set_hidden(s_status, true);
        } else if (page == AGENT_PAGE_VOICE) {
            build_voice();
        }
    }
    lv_screen_load(s_screen);
}

static void select_row(int row, bool selected)
{
    if (s_row_selected[row] == selected) return;
    s_row_selected[row] = selected;
    memo_ui_set_selected(s_rows[row], selected);
    lv_obj_set_style_bg_color(s_row_badges[row], lv_color_hex(selected ? UI_ACCENT : UI_ACCENT_SOFT), 0);
    lv_obj_set_style_text_color(s_row_initials[row], lv_color_hex(selected ? UI_SURFACE : UI_ACCENT), 0);
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
        char detail[80], initial[8];
        const agent_session_t *session = &s_view.sessions[i];
        const char *title = session->title[0] ? session->title : "新会话";
        set_text(s_row_titles[i], title, false);
        title_initial(title, initial, sizeof(initial));
        set_text(s_row_initials[i], initial, false);
        if (session->updated_at > 0) {
            const int offset = memo_settings_get()->tz_offset_min * 60;
            const time_t timestamp = session->updated_at / 1000 + offset;
            const time_t now = (time_t)memo_net_now() + offset;
            struct tm date, today;
            gmtime_r(&timestamp, &date);
            gmtime_r(&now, &today);
            // Today's sessions show only the time; earlier ones keep the date.
            if (memo_net_now() != 0 && date.tm_year == today.tm_year && date.tm_yday == today.tm_yday)
                snprintf(detail, sizeof(detail), "%02d:%02d", date.tm_hour, date.tm_min);
            else
                snprintf(detail, sizeof(detail), "%02d-%02d %02d:%02d", date.tm_mon + 1, date.tm_mday, date.tm_hour, date.tm_min);
        } else detail[0] = '\0';
        set_text(s_row_details[i], detail, false);
        select_row(i, i == s_selected);
    }
    set_text(s_title, s_view.notice[0] ? s_view.notice : "会话", false);
    if (lv_label_get_long_mode(s_title) != LV_LABEL_LONG_SCROLL_CIRCULAR)
        lv_label_set_long_mode(s_title, LV_LABEL_LONG_SCROLL_CIRCULAR);
}

// Progress reads in the accent color; failures that offer a retry read as alerts.
static uint32_t chat_status_color(const char *text)
{
    if (s_view.notice[0] != '\0' && strcmp(text, s_view.notice) == 0) return UI_DIM;
    const char *state = s_view.message.state;
    if (s_view.text_kind == AGENT_TEXT_DRAFT) return UI_ACCENT;
    if (s_view.text_kind == AGENT_TEXT_INPUT) return UI_DIM;
    if (strcmp(state, "failed") == 0 || strcmp(state, "unconfirmed") == 0 ||
        strcmp(state, "incomplete") == 0) return UI_RECORDING;
    if (agent_message_active(state)) return UI_ACCENT;
    return UI_DIM;
}

static void chat_status(const char *text)
{
    // LVGL's DOT mode edits its displayed buffer, so compare the source text.
    if (strcmp(s_chat_status, text) != 0 && memo_font_label_text(s_status, text, false))
        agent_copy_text(s_chat_status, sizeof(s_chat_status), text);
    const uint32_t color = chat_status_color(text);
    if ((lv_color_to_u32(lv_obj_get_style_text_color(s_status, 0)) & 0xFFFFFF) != color)
        lv_obj_set_style_text_color(s_status, lv_color_hex(color), 0);
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
    const uint32_t color = shown_input ? UI_ACCENT_SOFT : UI_SURFACE;
    const char *title = s_view.title[0] ? s_view.title : "对话";
    if (strcmp(s_chat_title, title) != 0 && memo_font_label_text(s_title, title, false))
        agent_copy_text(s_chat_title, sizeof(s_chat_title), title);
    set_text(s_chat_role, shown_input ? "我" : "助手", false);
    set_background(s_chat_card, color);
    set_background(s_scroll, color);
    set_background(s_chat_wait, color);
    set_background(s_chat_dot, shown_input ? UI_DIM : UI_ACCENT);

    agent_speech_copy(&s_speech);
    const bool speaking = s_speech.active && s_speech.audible;
    // Before the first words arrive the card waits; after that it follows the SSE text.
    if (generating && s_view.message.reply_bytes == 0) {
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
            const bool following = lv_obj_get_scroll_bottom(s_scroll) <= 8;
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
            // New words are followed only while the reader is already at the end.
            if (generating && following && !different) target = position + lv_obj_get_scroll_bottom(s_scroll);
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
    const bool reading = (!current && !has_text) || (loading && !has_text && !generating);
    chat_wait(reading);
    const char *status = input ? (draft ? "待发送" : "") : message_status(s_view.message.state);
    if (loading || reading) status = "正在读取";
    if (speaking && !input) status = "正在朗读";
    char position[48];
    if (status[0] == '\0' && s_view.turn.total > 1) {
        snprintf(position, sizeof(position), "第 %lu/%lu 轮", (unsigned long)s_view.turn.index, (unsigned long)s_view.turn.total);
        status = position;
    }
    if (!loading && !has_text && !input) {
        const char *empty = status[0] ? status : "回复为空";
        set_text(s_chat_body, empty, false);
    }
    chat_status(s_view.notice[0] ? s_view.notice : status);
}


typedef struct { const char *key; bool hold; const char *text; } voice_hint_t;

// Key hints: an outlined key is a short press, a filled key a long press.
static void voice_hints(int set, const voice_hint_t *items, int count)
{
    if (set == s_voice_hint_set) return;
    s_voice_hint_set = set;
    lv_obj_clean(s_voice_hints);
    for (int i = 0; i < count; i++) {
        lv_obj_t *item = bare(s_voice_hints);
        lv_obj_set_size(item, LV_SIZE_CONTENT, LV_SIZE_CONTENT);
        lv_obj_set_flex_flow(item, LV_FLEX_FLOW_ROW);
        lv_obj_set_flex_align(item, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
        lv_obj_set_style_pad_column(item, 5, 0);
        lv_obj_t *chip = bare(item);
        lv_obj_set_size(chip, LV_SIZE_CONTENT, 22);
        lv_obj_set_flex_flow(chip, LV_FLEX_FLOW_ROW);
        lv_obj_set_flex_align(chip, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);
        lv_obj_set_style_radius(chip, 6, 0);
        lv_obj_set_style_pad_hor(chip, 5, 0);
        lv_obj_set_style_border_width(chip, 2, 0);
        lv_obj_set_style_border_color(chip, lv_color_hex(items[i].hold ? UI_FG : UI_DIM), 0);
        lv_obj_set_style_bg_color(chip, lv_color_hex(UI_FG), 0);
        lv_obj_set_style_bg_opa(chip, items[i].hold ? LV_OPA_COVER : LV_OPA_TRANSP, 0);
        // Every key uses the same face and size; arrows are Montserrat's chevrons.
        lv_obj_t *key = memo_ui_label(chip, &lv_font_montserrat_14, items[i].hold ? UI_BG : UI_FG);
        if (strcmp(items[i].key, "OK") != 0) lv_obj_set_style_text_letter_space(key, 2, 0);
        lv_label_set_text_static(key, items[i].key);
        lv_obj_t *text = memo_ui_label(item, memo_font_label(), UI_DIM);
        lv_label_set_text_static(text, items[i].text);
    }
}

static void set_text_color(lv_obj_t *object, uint32_t color)
{
    if ((lv_color_to_u32(lv_obj_get_style_text_color(object, 0)) & 0xFFFFFF) != color)
        lv_obj_set_style_text_color(object, lv_color_hex(color), 0);
}

static void set_long_mode(lv_obj_t *object, lv_label_long_mode_t mode)
{
    if (lv_label_get_long_mode(object) != mode) lv_label_set_long_mode(object, mode);
}

static void reveal(lv_obj_t *object, bool visible)
{
    if (lv_obj_is_hidden(object) == visible) lv_obj_set_hidden(object, !visible);
}

// Bars inside the orb: heights in pixels for the big orb, scaled for the small one.
static void voice_bars(const int heights[3], uint32_t color, lv_opa_t opa)
{
    const int size = s_voice_small ? VOICE_ORB_SMALL : VOICE_ORB;
    const int width = s_voice_small ? 5 : 10, pitch = s_voice_small ? 9 : 18;
    for (int i = 0; i < AGENT_WAVE_BARS; i++) {
        int height = s_voice_small ? heights[i] * 44 / 100 : heights[i];
        if (height < width) height = width;
        const int x = size / 2 + (i - 1) * pitch - width / 2;
        const int y = size / 2 - height / 2;
        if (lv_obj_get_x(s_wave[i]) != x || lv_obj_get_y(s_wave[i]) != y) lv_obj_set_pos(s_wave[i], x, y);
        if (lv_obj_get_width(s_wave[i]) != width || lv_obj_get_height(s_wave[i]) != height) lv_obj_set_size(s_wave[i], width, height);
        set_background(s_wave[i], color);
        if (lv_obj_get_style_bg_opa(s_wave[i], 0) != opa) lv_obj_set_style_bg_opa(s_wave[i], opa, 0);
    }
}

static void voice_orb(bool small, uint32_t fill, lv_opa_t fill_opa, uint32_t ring, bool ringed)
{
    if (small != s_voice_small) {
        s_voice_small = small;
        const int size = small ? VOICE_ORB_SMALL : VOICE_ORB;
        lv_obj_set_pos(s_voice_orb, small ? VOICE_ORB_SMALL_X : VOICE_ORB_X, small ? VOICE_ORB_SMALL_Y : VOICE_ORB_Y);
        lv_obj_set_size(s_voice_orb, size, size);
    }
    set_background(s_voice_orb, fill);
    if (lv_obj_get_style_bg_opa(s_voice_orb, 0) != fill_opa) lv_obj_set_style_bg_opa(s_voice_orb, fill_opa, 0);
    const int outline = ringed ? 7 : 0;
    if (lv_obj_get_style_outline_width(s_voice_orb, 0) != outline) lv_obj_set_style_outline_width(s_voice_orb, outline, 0);
    if ((lv_color_to_u32(lv_obj_get_style_outline_color(s_voice_orb, 0)) & 0xFFFFFF) != ring)
        lv_obj_set_style_outline_color(s_voice_orb, lv_color_hex(ring), 0);
}

// One display sentence without Markdown emphasis, bounded for one label.
static void sentence_text(const char *text, uint16_t start, uint16_t end, char *out, size_t capacity)
{
    size_t used = 0;
    for (size_t i = start; i < end && used + 1 < capacity;) {
        if ((text[i] == '*' && text[i + 1] == '*') || (text[i] == '_' && text[i + 1] == '_')) { i += 2; continue; }
        if (text[i] == '`') { i++; continue; }
        const size_t step = agent_utf8_step(text, i);
        if (used + step >= capacity) break;
        memcpy(out + used, text + i, step);
        used += step;
        i += step;
    }
    out[used] = '\0';
}

static bool voice_has_draft(void)
{
    return s_view.text_kind == AGENT_TEXT_DRAFT && s_view.segment_total > 0;
}


static uint32_t utf8_codepoint(const char *text, size_t at, size_t *size)
{
    const unsigned char *c = (const unsigned char *)text + at;
    *size = agent_utf8_step(text, at);
    if (*size == 1) return c[0];
    if (*size == 2) return ((uint32_t)(c[0] & 0x1F) << 6) | (c[1] & 0x3F);
    if (*size == 3) return ((uint32_t)(c[0] & 0x0F) << 12) | ((uint32_t)(c[1] & 0x3F) << 6) | (c[2] & 0x3F);
    return ((uint32_t)(c[0] & 0x07) << 18) | ((uint32_t)(c[1] & 0x3F) << 12) | ((uint32_t)(c[2] & 0x3F) << 6) | (c[3] & 0x3F);
}

// End of the line starting at `start`: as much as fits the strip, keeping
// closing punctuation off the start of the next line.
static size_t wrap_end(const char *text, size_t start)
{
    static const char closers[] = "，。！？；：、）」』”’》】,.!?;:)\"'";
    const int32_t width = VOICE_STRIP_W - 28;
    int32_t line = 0;
    size_t at = start;
    while (text[at] && text[at] != '\n') {
        size_t size;
        const uint32_t letter = utf8_codepoint(text, at, &size);
        const int32_t glyph = lv_font_get_glyph_width(memo_font_label(), letter, 0);
        char piece[5] = {0};
        memcpy(piece, text + at, size);
        if (line > 0 && line + glyph > width && strstr(closers, piece) == NULL) break;
        line += glyph;
        at += size;
    }
    return at;
}

// Builds the review units: Markdown emphasis, code ticks and line leaders are
// dropped and blank lines collapse. Every sentence starts a unit, and a
// sentence wider than the strip continues in further units, so each up/down
// press shows one still, readable piece.
static void voice_wrap_lines(void)
{
    free(s_voice_display);
    s_voice_display = NULL;
    s_voice_line_count = 0;
    const size_t length = strlen(s_voice_reply);
    char *display = malloc(length + 1);
    if (display == NULL) return;
    size_t used = 0;
    bool line_start = true;
    for (size_t i = 0; i < length;) {
        if (line_start) {
            while (i < length && (s_voice_reply[i] == ' ' || s_voice_reply[i] == '#')) i++;
            if (i + 1 < length && strchr("-*>", s_voice_reply[i]) && s_voice_reply[i + 1] == ' ') i += 2;
            line_start = false;
            continue;
        }
        const char c = s_voice_reply[i];
        if ((c == '*' && s_voice_reply[i + 1] == '*') || (c == '_' && s_voice_reply[i + 1] == '_')) { i += 2; continue; }
        if (c == '`') { i++; continue; }
        if (c == '\n') {
            if (used > 0 && display[used - 1] != '\n') display[used++] = '\n';
            line_start = true;
            i++;
            continue;
        }
        display[used++] = s_voice_reply[i++];
    }
    display[used] = '\0';
    s_voice_display = display;
    static const char closers[] = "，。！？；：、）」』”’》】,.!?;:)\"'";
    const lv_font_t *font = memo_font_label();
    const int32_t width = VOICE_STRIP_W - 28;
    size_t at = 0;
    while (at < used && s_voice_line_count < VOICE_MAX_LINES && at <= UINT16_MAX) {
        while (at < used && (display[at] == ' ' || display[at] == '\n')) at++;
        if (at >= used) break;
        s_voice_lines[s_voice_line_count++] = (uint16_t)at;
        int32_t line = 0;
        while (at < used && display[at] != '\n') {
            size_t size;
            const uint32_t letter = utf8_codepoint(display, at, &size);
            const int32_t glyph = lv_font_get_glyph_width(font, letter, 0);
            char piece[5] = {0};
            memcpy(piece, display + at, size);
            // Closing punctuation never starts a unit; it stays with the text before it.
            const bool closer = strstr(closers, piece) != NULL;
            if (line > 0 && line + glyph > width && !closer) break;
            line += glyph;
            const size_t was = at;
            at += size;
            if (agent_sentence_end(display, was, size)) {
                for (;;) { // Keep closing quotes and brackets with the sentence.
                    size_t next;
                    if (at >= used) break;
                    utf8_codepoint(display, at, &next);
                    char tail[5] = {0};
                    memcpy(tail, display + at, next);
                    if (strstr("”’」』）)\"'】》", tail) == NULL) break;
                    at += next;
                }
                break;
            }
        }
    }
    s_voice_lines[s_voice_line_count] = (uint16_t)(at < UINT16_MAX ? at : UINT16_MAX);
}


enum { TICKER_RUNE_MS = 220, TICKER_MIN_MS = 1800, TICKER_MAX_MS = 5000, TICKER_LOOP_MS = 3000, SLIDE_MS = 250 };

static void slide_anim(void *object, int32_t value)
{
    lv_obj_set_style_translate_y(object, value, 0);
    lv_obj_set_style_opa(object, (lv_opa_t)(LV_OPA_COVER - (value < 0 ? -value : value) * 18), 0);
}

// A new line slides in from the direction of travel (up for the next line).
static void line_slide(int direction)
{
    lv_anim_t animation;
    lv_anim_init(&animation);
    lv_anim_set_var(&animation, s_voice_strip_text);
    lv_anim_set_exec_cb(&animation, slide_anim);
    lv_anim_set_values(&animation, direction * 12, 0);
    lv_anim_set_duration(&animation, SLIDE_MS);
    lv_anim_set_path_cb(&animation, lv_anim_path_ease_out);
    lv_anim_start(&animation);
}

// Time a line stays before the next: long enough to read it.
static int64_t line_dwell(int index)
{
    if (s_voice_display == NULL || index < 0 || index >= s_voice_line_count) return (int64_t)TICKER_MIN_MS * 1000;
    size_t runes = 0;
    for (size_t at = s_voice_lines[index]; at < s_voice_lines[index + 1]; at++)
        if (((unsigned char)s_voice_display[at] & 0xC0) != 0x80) runes++;
    int64_t ms = (int64_t)runes * TICKER_RUNE_MS;
    if (ms < TICKER_MIN_MS) ms = TICKER_MIN_MS;
    if (ms > TICKER_MAX_MS) ms = TICKER_MAX_MS;
    return ms * 1000;
}

// Advances the resting ticker; returns the line to show.
static int ticker_line(int64_t now)
{
    if (s_ticker_source != s_voice_display || s_ticker_line < 0 || s_ticker_line >= s_voice_line_count) {
        s_ticker_source = s_voice_display;
        s_ticker_line = 0;
        s_ticker_next = now + line_dwell(0);
        return 0;
    }
    if (s_voice_review < 0 && s_voice_line_count > 1 && now >= s_ticker_next) {
        const bool wrap = s_ticker_line + 1 >= s_voice_line_count;
        s_ticker_line = wrap ? 0 : s_ticker_line + 1;
        // After the last line, rest a little before starting over.
        s_ticker_next = now + line_dwell(s_ticker_line) + (wrap ? TICKER_LOOP_MS * 1000 : 0);
        line_slide(1);
    }
    return s_ticker_line;
}

// Keeps a copy of the newest reply and its sentence ranges for the strip.
static void voice_reply_cache(bool streaming)
{
    static int64_t copied;
    char key[sizeof(s_voice_reply_key)];
    snprintf(key, sizeof(key), "%lu/%lu/%s/%lu/%lu", (unsigned long)s_view.selection, (unsigned long)s_view.text_generation,
             s_view.message.id, (unsigned long)s_view.message.revision, (unsigned long)s_view.message.reply_bytes);
    if (strcmp(key, s_voice_reply_key) == 0) return;
    // A growing reply is copied at most twice a second to spare the heap.
    const int64_t now = esp_timer_get_time();
    if (streaming && s_voice_reply != NULL && now - copied < 500000) return;
    copied = now;
    char *reply = agent_client_part_copy(s_view.selection, false);
    if (reply == NULL) return; // Not loaded yet; keep the last copy.
    memo_font_filter_text(reply, false);
    free(s_voice_reply);
    s_voice_reply = reply;
    agent_copy_text(s_voice_reply_key, sizeof(s_voice_reply_key), key);
    s_voice_sentences = agent_sentence_split(s_voice_reply, s_voice_starts, s_voice_ends, AGENT_VOICE_MAX_SENTENCES);
    voice_wrap_lines();
    if (s_voice_review >= s_voice_line_count) s_voice_review = -1;
}

static void render_voice(void)
{
    const char *title = s_view.title[0] ? s_view.title : "对话";
    if (strcmp(s_chat_title, title) != 0 && memo_font_label_text(s_title, title, false))
        agent_copy_text(s_chat_title, sizeof(s_chat_title), title);
    agent_speech_copy(&s_speech);
    const bool speaking = s_speech.active && s_speech.audible && strcmp(s_speech.message_id, s_view.message.id) == 0;
    const agent_voice_mode_t mode = agent_voice_mode((int)s_view.capture, voice_has_draft(), s_view.turn.present,
                                                     s_view.message.state, speaking, s_view.message.reply_bytes > 0);
    // Only the states whose strip shows reply sentences need the reply text.
    if (s_view.turn.present && (mode == AGENT_VOICE_IDLE || mode == AGENT_VOICE_FAILED || mode == AGENT_VOICE_REPLYING))
        voice_reply_cache(mode == AGENT_VOICE_REPLYING);
    if ((int)mode != s_voice_mode) {
        agent_trace(TRACE_VOICE, (uint16_t)mode, 0, NULL);
        if (mode != AGENT_VOICE_IDLE && mode != AGENT_VOICE_FAILED) s_voice_review = -1;
    }
    s_voice_mode = (int)mode;
    const int64_t now = esp_timer_get_time();
    if (s_voice_review >= s_voice_line_count) s_voice_review = -1; // The reply changed under the review.
    if (s_voice_review >= 0 && now - s_voice_review_at > 15000000) { // Resume ticking from this line.
        s_ticker_line = s_voice_review;
        s_ticker_next = now + line_dwell(s_voice_review);
        s_voice_review = -1;
    }
    char line[256];

    // Orb and bars.
    static const int idle_bars[3] = {26, 44, 26};
    if (mode == AGENT_VOICE_RECORDING || mode == AGENT_VOICE_SPEAKING) {
        const uint8_t level = mode == AGENT_VOICE_RECORDING ? agent_capture_level() : s_speech.level;
        s_levels[0] = level > s_levels[0] ? level : (uint8_t)(s_levels[0] * 3 / 4);
        const int bars[3] = {14 + s_levels[0] * 26 / 100, 14 + s_levels[0] * 46 / 100, 14 + s_levels[0] * 26 / 100};
        const uint32_t color = mode == AGENT_VOICE_RECORDING ? UI_RECORDING : UI_ACCENT;
        voice_orb(false, mode == AGENT_VOICE_RECORDING ? UI_RECORDING : UI_ACCENT_SOFT,
                  mode == AGENT_VOICE_RECORDING ? LV_OPA_20 : LV_OPA_COVER, color, true);
        voice_bars(bars, color, LV_OPA_COVER);
    } else if (mode == AGENT_VOICE_THINKING) {
        // Bars rise one after another, slowly, while the arc orbits.
        int bars[3];
        for (int i = 0; i < 3; i++) {
            const int phase = (int)((now / 1000 + 1600 - i * 200) % 1600);
            const int rise = phase < 560 ? phase * 100 / 560 : phase < 1120 ? (1120 - phase) * 100 / 560 : 0;
            bars[i] = 14 + rise * (i == 1 ? 30 : 16) / 100;
        }
        voice_orb(false, UI_ACCENT_SOFT, LV_OPA_COVER, UI_ACCENT, false);
        voice_bars(bars, UI_ACCENT, LV_OPA_COVER);
    } else {
        voice_orb(mode == AGENT_VOICE_DRAFT, UI_ACCENT_SOFT, LV_OPA_COVER, UI_ACCENT, false);
        voice_bars(idle_bars, UI_ACCENT, mode == AGENT_VOICE_TRANSCRIBING ? LV_OPA_COVER : 140);
    }
    spin(s_voice_arc, mode == AGENT_VOICE_THINKING, &s_voice_arc_spinning, 1800);
    spin(s_voice_strip_spin, mode == AGENT_VOICE_TRANSCRIBING, &s_voice_spin_spinning, 900);

    // Status line (or timer, or the side labels next to the small orb).
    const char *status = "";
    uint32_t tone = UI_FG;
    reveal(s_voice_timer, mode == AGENT_VOICE_RECORDING);
    reveal(s_voice_side, mode == AGENT_VOICE_DRAFT);
    reveal(s_voice_side_sub, mode == AGENT_VOICE_DRAFT);
    switch (mode) {
    case AGENT_VOICE_PREPARING: status = "正在准备"; tone = UI_DIM; break;
    case AGENT_VOICE_RECORDING: {
        const uint32_t seconds = agent_capture_duration_ms() / 1000;
        snprintf(line, sizeof(line), "%02lu:%02lu", (unsigned long)(seconds / 60), (unsigned long)(seconds % 60));
        set_text(s_voice_timer, line, true);
        break;
    }
    case AGENT_VOICE_TRANSCRIBING: status = "正在转写"; tone = UI_DIM; break;
    case AGENT_VOICE_DRAFT:
        set_text(s_voice_side, "草稿待发送", false);
        set_text(s_voice_side_sub, "长按 ↓ 清空", false);
        break;
    case AGENT_VOICE_THINKING:
        if (strcmp(s_think_id, s_view.message.id) != 0) {
            agent_copy_text(s_think_id, sizeof(s_think_id), s_view.message.id);
            s_think_since = now;
        }
        snprintf(line, sizeof(line), "Hermes 正在思考 %llus", (unsigned long long)((now - s_think_since) / 1000000));
        status = line;
        break;
    case AGENT_VOICE_SPEAKING: status = "正在朗读"; break;
    case AGENT_VOICE_REPLYING: status = "正在回复"; break;
    case AGENT_VOICE_FAILED: status = message_status(s_view.message.state); tone = UI_RECORDING; break;
    case AGENT_VOICE_IDLE:
        if (s_voice_review >= 0) {
            snprintf(line, sizeof(line), "上一轮回复 %d/%d", s_voice_review + 1, s_voice_line_count);
            status = line;
            tone = UI_DIM;
        }
        else status = s_view.turn.present ? "按住 OK 继续说" : "按住 OK 说话";
        break;
    }
    if (s_view.notice[0] != '\0' && mode != AGENT_VOICE_RECORDING && mode != AGENT_VOICE_DRAFT) {
        status = s_view.notice;
        tone = strcmp(s_view.notice, "草稿已清空") == 0 ? UI_DIM : UI_RECORDING;
    }
    reveal(s_voice_status, mode != AGENT_VOICE_RECORDING && mode != AGENT_VOICE_DRAFT);
    if (strcmp(lv_label_get_text(s_voice_status), status) != 0) set_text(s_voice_status, status, false);
    set_text_color(s_voice_status, tone);

    // The strip: one line of words, or the expanded draft panel.
    bool strip = false, tag = false, circular = false;
    uint32_t strip_tone = UI_FG, border = UI_BORDER;
    line[0] = '\0';
    if (mode == AGENT_VOICE_RECORDING || mode == AGENT_VOICE_TRANSCRIBING) {
        strip = true;
        border = mode == AGENT_VOICE_RECORDING ? UI_RECORDING : UI_BORDER;
        if (s_view.text.text[0] != '\0') {
            const char *tail = agent_utf8_tail(s_view.text.text, VOICE_TAIL_RUNES);
            const size_t lead = tail == s_view.text.text ? 0 : strlen("…");
            memcpy(line, "…", lead);
            agent_copy_text(line + lead, sizeof(line) - lead, tail);
        } else {
            agent_copy_text(line, sizeof(line), mode == AGENT_VOICE_RECORDING ? "请开始说话" : "…");
            strip_tone = UI_DIM;
        }
    } else if (mode == AGENT_VOICE_THINKING) {
        if (strcmp(s_voice_question_id, s_view.message.id) != 0) {
            char *question = agent_client_part_copy(s_view.selection, true);
            if (question != NULL) {
                memo_font_filter_text(question, false);
                for (char *c = question; *c; c++) if (*c == '\n') *c = ' ';
                free(s_voice_question);
                s_voice_question = question;
                agent_copy_text(s_voice_question_id, sizeof(s_voice_question_id), s_view.message.id);
            }
        }
        strip = s_voice_question != NULL && s_voice_question[0] != '\0';
        if (strip) agent_copy_text(line, sizeof(line), s_voice_question);
        strip_tone = UI_DIM;
    } else if (mode == AGENT_VOICE_SPEAKING) {
        // Like the resting ticker, a long sentence shows one whole line at a
        // time; lines advance at the estimated speech rate.
        if (s_speech.index != s_speak_index || strcmp(s_speech.sentence, s_speak_text) != 0) {
            if (s_speak_text[0] != '\0') line_slide(1); // The next sentence rises in.
            agent_copy_text(s_speak_text, sizeof(s_speak_text), s_speech.sentence);
            memo_font_filter_text(s_speak_text, false);
            s_speak_index = s_speech.index;
            s_speak_at = now;
            s_speak_line = 0;
        }
        size_t start = 0, end = wrap_end(s_speak_text, 0), spoken = 0;
        int index = 0;
        for (;;) {
            size_t runes = 0;
            for (size_t at = start; at < end; at++) if (((unsigned char)s_speak_text[at] & 0xC0) != 0x80) runes++;
            if (s_speak_text[end] == '\0' || (int64_t)(spoken + runes) * SPEAK_RUNE_US > now - s_speak_at) break;
            spoken += runes;
            start = end;
            while (s_speak_text[start] == ' ' || s_speak_text[start] == '\n') start++;
            end = wrap_end(s_speak_text, start);
            index++;
        }
        if (index != s_speak_line) { s_speak_line = index; line_slide(1); }
        const size_t size = end - start < sizeof(line) - 1 ? end - start : sizeof(line) - 1;
        memcpy(line, s_speak_text + start, size);
        line[size] = '\0';
        strip = line[0] != '\0';
        border = UI_ACCENT;
    } else if ((mode == AGENT_VOICE_IDLE || mode == AGENT_VOICE_FAILED) && s_view.turn.present &&
               s_voice_display != NULL && s_voice_line_count > 0) {
        // At rest a ticker steps through the reply line by line; while the
        // reader holds it, the chosen line stays at full contrast.
        const int index = s_voice_review >= 0 && s_voice_review < s_voice_line_count ? s_voice_review : ticker_line(now);
        size_t start = s_voice_lines[index], end = s_voice_lines[index + 1];
        while (end > start && (s_voice_display[end - 1] == '\n' || s_voice_display[end - 1] == ' ')) end--;
        const size_t size = end - start < sizeof(line) - 1 ? end - start : sizeof(line) - 1;
        memcpy(line, s_voice_display + start, size);
        line[size] = '\0';
        strip = true;
        strip_tone = s_voice_review >= 0 ? UI_FG : UI_DIM;
    } else if (mode == AGENT_VOICE_REPLYING && s_voice_sentences > 0) {
        const int index = s_voice_sentences - 1;
        sentence_text(s_voice_reply, s_voice_starts[index], s_voice_ends[index], line, sizeof(line));
        strip = line[0] != '\0';
        strip_tone = UI_DIM;
    }
    reveal(s_voice_strip, strip);
    if (strip) {
        reveal(s_voice_strip_tag, tag);
        const int text_x = mode == AGENT_VOICE_TRANSCRIBING ? 38 : 14;
        const int text_w = VOICE_STRIP_W - text_x - (tag ? 56 : 14);
        if (lv_obj_get_x(s_voice_strip_text) != text_x) lv_obj_set_x(s_voice_strip_text, text_x);
        if (lv_obj_get_width(s_voice_strip_text) != text_w) lv_obj_set_width(s_voice_strip_text, text_w);
        set_long_mode(s_voice_strip_text, circular ? LV_LABEL_LONG_SCROLL_CIRCULAR : LV_LABEL_LONG_DOT);
        if (strcmp(lv_label_get_text(s_voice_strip_text), line) != 0) set_text(s_voice_strip_text, line, false);
        set_text_color(s_voice_strip_text, strip_tone);
        if ((lv_color_to_u32(lv_obj_get_style_border_color(s_voice_strip, 0)) & 0xFFFFFF) != border)
            lv_obj_set_style_border_color(s_voice_strip, lv_color_hex(border), 0);
    }

    reveal(s_voice_panel, mode == AGENT_VOICE_DRAFT);
    if (mode == AGENT_VOICE_DRAFT &&
        (s_voice_draft_selection != s_view.selection || s_voice_draft_generation != s_view.text_generation)) {
        char *draft = agent_client_text_copy(s_view.selection, s_view.text_generation);
        if (draft != NULL) {
            memo_font_filter_text(draft, false);
            lv_label_set_text_static(s_voice_panel_body, draft);
            free(s_voice_draft);
            s_voice_draft = draft;
            s_voice_draft_selection = s_view.selection;
            s_voice_draft_generation = s_view.text_generation;
            size_t runes = 0;
            for (const char *c = draft; *c; c++) if (((unsigned char)*c & 0xC0) != 0x80) runes++;
            char count[24];
            snprintf(count, sizeof(count), "%u 字", (unsigned)runes);
            set_text(s_voice_panel_count, count, false);
            lv_obj_update_layout(s_voice_panel_scroll);
            lv_obj_scroll_to_y(s_voice_panel_scroll, 0, LV_ANIM_OFF);
        }
    }

    // Key hints for what the keys do right now.
    static const voice_hint_t idle[] = {{"OK", false, "文字"}, {"OK", true, "说话"}};
    static const voice_hint_t review[] = {{LV_SYMBOL_UP LV_SYMBOL_DOWN, false, "逐行"}, {"OK", true, "说话"}};
    static const voice_hint_t failed[] = {{"OK", false, "重试"}, {"OK", true, "说话"}};
    static const voice_hint_t draft[] = {{"OK", false, "发送"}, {"OK", true, "重录"}};
    static const voice_hint_t recording[] = {{"OK", true, "松开结束"}};
    static const voice_hint_t speaking_hints[] = {{"OK", false, "文字"}, {LV_SYMBOL_DOWN, true, "停止"}};
    switch (mode) {
    case AGENT_VOICE_PREPARING:
    case AGENT_VOICE_RECORDING: voice_hints(1, recording, 1); break;
    case AGENT_VOICE_TRANSCRIBING: voice_hints(2, NULL, 0); break;
    case AGENT_VOICE_DRAFT: voice_hints(3, draft, 2); break;
    case AGENT_VOICE_SPEAKING: voice_hints(4, speaking_hints, 2); break;
    case AGENT_VOICE_FAILED: voice_hints(5, failed, 2); break;
    default:
        if (s_voice_review >= 0) voice_hints(6, review, 2);
        else voice_hints(7, idle, 2);
        break;
    }
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
        if (s_page != AGENT_PAGE_VOICE) build_screen(AGENT_PAGE_VOICE);
    } else if (s_record_requested) {
        const bool settled = s_view.capture == AGENT_IDLE;
        const agent_page_t next = settled && !s_record_dismissed && s_seen_capture != s_view.capture_serial ?
            AGENT_PAGE_VOICE : s_record_return;
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
    else if (s_page == AGENT_PAGE_VOICE) render_voice();
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
    } else if (s_page == AGENT_PAGE_CHAT || s_page == AGENT_PAGE_VOICE) {
        // Long Down leaves the current state: speaking, then the draft, then the session.
        agent_speech_copy(&s_speech);
        if (s_speech.active) { agent_speech_stop(false); return; }
        if (s_page == AGENT_PAGE_VOICE && voice_has_draft()) {
            const bool cleared = agent_client_clear_draft() == ESP_OK;
            if (bsp_lvgl_lock(1000)) { ui_notice(cleared ? "草稿已清空" : "暂时无法清空草稿"); bsp_lvgl_unlock(); }
            return;
        }
        if (agent_client_close_session() == ESP_OK) show_page(target);
    } else if (s_page != target) show_page(target);
}

// Text card: up/down scroll the message; at its edge a second press of the same
// key within half a second moves to the neighbouring message, which always
// opens at its top.
static bsp_btn_t s_edge_button;
static int64_t s_edge_at;

static void chat_hint(const char *text)
{
    agent_copy_text(s_local_notice, sizeof(s_local_notice), text);
    s_notice_until = text[0] ? esp_timer_get_time() + 1200000 : 0;
    agent_copy_text(s_view.notice, sizeof(s_view.notice), text);
    if (s_page == AGENT_PAGE_CHAT) chat_status(text);
}

static void scroll_chat(bsp_btn_t button)
{
    if (!bsp_lvgl_lock(1000)) return;
    const int direction = button == BSP_BTN_UP ? -1 : 1;
    const bool input = s_view.text_kind == AGENT_TEXT_INPUT;
    const bool draft = s_view.text_kind == AGENT_TEXT_DRAFT;
    lv_obj_update_layout(s_scroll);
    const int32_t position = lv_obj_get_scroll_y(s_scroll);
    const int32_t remaining = lv_obj_get_scroll_bottom(s_scroll);
    // About 70 % of the visible area per press, in whole lines.
    const int32_t pitch = memo_font_label()->line_height + AGENT_CHAT_LINE_SPACE;
    int32_t step = lv_obj_get_height(s_scroll) * 7 / 10 / pitch * pitch;
    if (step < pitch) step = pitch;
    if (direction < 0 && position > 0) {
        lv_obj_scroll_to_y(s_scroll, position > step ? position - step : 0, LV_ANIM_OFF);
        s_edge_at = 0;
    } else if (direction > 0 && remaining > 0) {
        lv_obj_scroll_to_y(s_scroll, position + (remaining > step ? step : remaining), LV_ANIM_OFF);
        s_edge_at = 0;
    } else if (!draft && !s_view.chat_loading && !s_view.text_loading) {
        const int64_t now = esp_timer_get_time();
        // Input comes before its reply; exchanges continue through before/after.
        const bool first = direction < 0 && input && s_view.turn.before[0] == '\0';
        const bool last = direction > 0 && !input && s_view.turn.after[0] == '\0';
        if (first || last) {
            s_edge_at = 0;
            chat_hint(first ? "已是第一条" : "已是最后一条");
        } else if (s_edge_button != button || s_edge_at == 0 || now - s_edge_at > 500000) {
            s_edge_button = button;
            s_edge_at = now;
            chat_hint(direction < 0 ? "双击 ↑ 看上一条" : "双击 ↓ 看下一条");
        } else {
            s_edge_at = 0;
            chat_hint("");
            s_chat_open_bottom = false;
            if (direction < 0 && !input) agent_client_message_select(true);
            else if (direction > 0 && input) agent_client_message_select(false);
            else agent_client_history(direction);
        }
    }
    bsp_lvgl_unlock();
}


// Back to the voice view from the text card, on the newest exchange's reply.
static void leave_text_card(void)
{
    if (s_view.turn.present && s_view.turn.after[0] != '\0') agent_client_show_latest();
    else if (s_view.text_kind == AGENT_TEXT_INPUT) agent_client_message_select(false);
    show_page(AGENT_PAGE_VOICE);
}

static void voice_key(bsp_btn_t button)
{
    const agent_voice_mode_t mode = (agent_voice_mode_t)s_voice_mode;
    if (button == BSP_BTN_OK) {
        if (mode == AGENT_VOICE_DRAFT) {
            if (agent_client_send(s_view.text.revision, s_view.selection) != ESP_OK && bsp_lvgl_lock(1000)) {
                ui_notice("内容已更新，请稍候");
                bsp_lvgl_unlock();
            }
        } else if (mode == AGENT_VOICE_FAILED) {
            agent_client_retry(s_view.message.id, s_view.selection);
        } else if (mode != AGENT_VOICE_TRANSCRIBING) {
            show_page(AGENT_PAGE_CHAT);
        }
        return;
    }
    if (!bsp_lvgl_lock(1000)) return;
    const int direction = button == BSP_BTN_UP ? -1 : 1;
    if (mode == AGENT_VOICE_DRAFT) {
        lv_obj_update_layout(s_voice_panel_scroll);
        const int32_t position = lv_obj_get_scroll_y(s_voice_panel_scroll);
        const int32_t step = 2 * memo_font_label()->line_height + 2 * AGENT_CHAT_LINE_SPACE;
        int32_t target = position + direction * step;
        const int32_t bottom = position + lv_obj_get_scroll_bottom(s_voice_panel_scroll);
        if (target > bottom) target = bottom;
        if (target < 0) target = 0;
        lv_obj_scroll_to_y(s_voice_panel_scroll, target, LV_ANIM_OFF);
    } else if ((mode == AGENT_VOICE_IDLE || mode == AGENT_VOICE_FAILED) && s_voice_line_count > 0) {
        // The first press holds the line in view; then each press moves one
        // line. Past the last line the ticker resumes from the start.
        s_voice_review_at = esp_timer_get_time();
        if (s_voice_review < 0) {
            s_voice_review = s_ticker_line >= 0 && s_ticker_line < s_voice_line_count ? s_ticker_line : 0;
        } else {
            const int index = s_voice_review + direction;
            if (index < 0) s_voice_review = 0;
            else if (index >= s_voice_line_count) {
                s_voice_review = -1;
                s_ticker_line = -1;
            } else {
                s_voice_review = index;
                line_slide(direction);
            }
        }
        render();
    }
    bsp_lvgl_unlock();
}

static void gesture(bsp_btn_t button, bool hold)
{
    char page[AGENT_TRACE_TAG_BYTES];
    snprintf(page, sizeof(page), "p%d c%d", (int)s_page, (int)s_view.capture);
    agent_trace(TRACE_GESTURE, (uint16_t)button, hold ? 1 : 0, page);
    if (s_sleep_requested) return;
    if (hold && button == BSP_BTN_DOWN) {
        // While OK is held to record, or the transcript settles, Down does nothing.
        if (s_view.capture == AGENT_IDLE) exit_page();
        return;
    }
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
    if (s_view.capture != AGENT_IDLE) return;
    if (hold && button == BSP_BTN_OK && agent_page_can_record(s_page)) {
        s_record_return = s_page;
        s_seen_capture = s_view.capture_serial;
        agent_speech_stop(true); // Recording interrupts a reply being read aloud.
        if (agent_client_record(s_page == AGENT_PAGE_SESSIONS) == ESP_OK) {
            s_record_requested = true;
            s_record_dismissed = false;
            show_page(AGENT_PAGE_VOICE);
        } else if (bsp_lvgl_lock(1000)) { ui_notice("请先连接服务器"); bsp_lvgl_unlock(); }
        return;
    }
    if (s_page == AGENT_PAGE_SESSIONS) {
        if (hold) return;
        if (button == BSP_BTN_OK) {
            if (s_selected < s_view.session_count && agent_client_open(&s_view.sessions[s_selected]) == ESP_OK)
                show_page(AGENT_PAGE_VOICE);
        } else {
            const agent_step_t step = agent_page_step(s_selected, s_view.session_count, button == BSP_BTN_UP ? -1 : 1,
                s_view.session_offset > 0, s_view.sessions_more);
            s_selected = step.selected;
            if (step.direction != 0) agent_client_sessions_page(step.direction);
        }
    } else if (s_page == AGENT_PAGE_CHAT && !hold) {
        if (button == BSP_BTN_OK) leave_text_card();
        else scroll_chat(button);
    } else if (s_page == AGENT_PAGE_VOICE && !hold) {
        voice_key(button);
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

// Reads aloud each new newest exchange of the open session: one sent from this
// device or from the web page. Opening a session does not replay its last reply.
static void follow_speech(void)
{
    if (strcmp(s_view.session_id, s_play_session) != 0) {
        agent_speech_stop(false);
        agent_copy_text(s_play_session, sizeof(s_play_session), s_view.session_id);
        s_play_seen[0] = '\0';
        s_play_baseline = true;
    }
    if (!s_view.speech || s_view.session_id[0] == '\0' || !s_view.turn.present || s_view.capture != AGENT_IDLE ||
        s_view.session_switching || s_view.chat_loading || s_view.turn.after[0] != '\0') return;
    const char *id = s_view.turn.message.id;
    if (id[0] == '\0' || strcmp(id, s_play_seen) == 0) return;
    const bool baseline = s_play_baseline;
    s_play_baseline = false;
    agent_copy_text(s_play_seen, sizeof(s_play_seen), id);
    if (baseline && !agent_message_active(s_view.turn.message.state)) return;
    agent_speech_play(s_view.session_id, id);
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
        follow_speech();
        const int64_t now = esp_timer_get_time();
        if (now - last_memory >= 5000000 || s_view.capture != last_capture || (int)s_page != last_page) {
            ESP_LOGI(TAG, "memory page=%d capture=%d free=%u largest=%u minimum=%u input_stack=%u",
                     (int)s_page, (int)s_view.capture,
                     (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                     (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
                     (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT),
                     (unsigned)uxTaskGetStackHighWaterMark(NULL));
            if ((int)s_page != last_page) agent_trace(TRACE_PAGE, (uint16_t)s_page, 0, NULL);
            if (s_view.capture != last_capture) {
                agent_trace(TRACE_CAPTURE, (uint16_t)s_view.capture, 0, NULL);
                agent_trace(TRACE_HEAP, (uint16_t)(heap_caps_get_largest_free_block(MALLOC_CAP_8BIT) / 1024),
                            (int32_t)heap_caps_get_free_size(MALLOC_CAP_8BIT), NULL);
            }
            last_memory = now;
            last_capture = s_view.capture;
            last_page = s_page;
        }
        const bool discard_input = atomic_exchange(&s_release_lost, false);
        if (discard_input) {
            agent_trace(TRACE_KEY, 0xff, 0, "lost"); // A release was dropped; key state is reset.
            xQueueReset(s_input);
            agent_client_release_record();
            memset(s_keys, 0, sizeof(s_keys));
            s_wake_ok = false;
        }
        if (received && !discard_input) {
            agent_trace(TRACE_KEY, (uint16_t)input.button, (int32_t)input.event, NULL);
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
    agent_trace_init(); // First, so later warnings and the reset reason are retained.
    ESP_LOGI(TAG, "voice agent terminal starting");
    if (memo_settings_init() != ESP_OK || bsp_i2c_init() != ESP_OK || bsp_display_init() != ESP_OK || bsp_lvgl_init() == NULL) return;
    bsp_display_backlight(memo_settings_get()->brightness);
    if (bsp_audio_init() != ESP_OK) ESP_LOGW(TAG, "audio initialization failed");
    bsp_audio_set_volume(memo_settings_get()->volume);
    bsp_battery_init();
    if (memo_net_start() != ESP_OK || memo_discovery_init() != ESP_OK) ESP_LOGW(TAG, "network initialization failed");
    if (agent_capture_init() != ESP_OK || agent_client_init() != ESP_OK) { ESP_LOGE(TAG, "worker allocation failed"); return; }
    if (agent_speech_init() != ESP_OK) ESP_LOGW(TAG, "speech playback unavailable");
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

