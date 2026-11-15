// Host UI preview. Includes the production application translation unit so
// screens are built by the real build_screen()/render() code, then rendered
// with LVGL's software renderer into RGB565 frames written as raw RGB888 files.
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "../../main/agent_app.c"
#include "bsp_display_rounding.h"

// ---- Scenario state -------------------------------------------------------
static memo_settings_t g_settings = {.volume = 60, .brightness = 70, .screen_timeout_s = 30,
                                     .sleep_min = 10, .tz_offset_min = 480, .theme = MEMO_THEME_DARK};
static bool g_has_ip = true;
static memo_net_state_t g_net = MEMO_NET_CONNECTED;
static memo_provision_state_t g_prov = MEMO_PROV_ADVERTISING;
static const char *g_chat_text = "";
static uint8_t g_level;
static uint32_t g_duration_ms = 83000;

// ---- Stubs for functions reached by rendering -----------------------------
const memo_settings_t *memo_settings_get(void) { return &g_settings; }
uint64_t memo_net_now(void) { return 1794703440; } // 10:04 at UTC+8
bool memo_net_has_ip(void) { return g_has_ip; }
memo_net_state_t memo_net_state(void) { return g_net; }
void memo_net_info(memo_network_info_t *out) { *out = (memo_network_info_t){"ABCDEF", "192.168.31.116"}; }
memo_discovery_state_t memo_discovery_state(void) { return MEMO_DISCOVERY_FOUND; }
bool memo_discovery_endpoint(memo_endpoint_t *out)
{
    *out = (memo_endpoint_t){.host = "192.168.31.2", .port = 8080, .confirmed = true};
    return true;
}
bool agent_client_discovery_pending(void) { return false; }
memo_provision_state_t memo_provision_state(void) { return g_prov; }
void memo_provision_info(memo_network_info_t *out) { *out = (memo_network_info_t){"ABCDEF", "192.168.31.116"}; }
esp_err_t memo_provision_error(void) { return ESP_FAIL; }
const char *esp_err_to_name(esp_err_t code) { (void)code; return "ESP_FAIL"; }
char *agent_client_text_copy(uint32_t selection, uint32_t generation)
{
    (void)selection; (void)generation;
    return strdup(g_chat_text);
}
uint32_t agent_capture_duration_ms(void) { return g_duration_ms; }
uint8_t agent_capture_level(void) { return g_level; }
int64_t esp_timer_get_time(void) { return 0; }
esp_err_t agent_client_close_session(void) { return ESP_OK; }

// ---- Display ---------------------------------------------------------------
static uint16_t g_frame[BSP_LCD_W * BSP_LCD_H];
static uint8_t g_draw[BSP_LCD_W * BSP_LCD_H * 2];

static void flush(lv_display_t *display, const lv_area_t *area, uint8_t *pixels)
{
    const int width = lv_area_get_width(area);
    for (int y = area->y1; y <= area->y2; y++)
        memcpy(&g_frame[y * BSP_LCD_W + area->x1], pixels + (size_t)(y - area->y1) * width * 2, (size_t)width * 2);
    lv_display_flush_ready(display);
}

static void save(const char *directory, const char *name)
{
    lv_obj_invalidate(lv_screen_active());
    lv_refr_now(NULL);
    char path[512];
    snprintf(path, sizeof(path), "%s/%s.rgb", directory, name);
    FILE *file = fopen(path, "wb");
    if (file == NULL) { perror(path); exit(1); }
    for (int y = 0; y < BSP_LCD_H; y++) {
        for (int x = 0; x < BSP_LCD_W; x++) {
            uint8_t rgb[3] = {0, 0, 0};
            if (!bsp_display_pixel_outside_rounded_rect(x, y, BSP_LCD_W, BSP_LCD_H, BSP_LVGL_SCREEN_RADIUS)) {
                const uint16_t c = g_frame[y * BSP_LCD_W + x];
                rgb[0] = (uint8_t)(((c >> 11) & 0x1F) * 255 / 31);
                rgb[1] = (uint8_t)(((c >> 5) & 0x3F) * 255 / 63);
                rgb[2] = (uint8_t)((c & 0x1F) * 255 / 31);
            }
            fwrite(rgb, 1, 3, file);
        }
    }
    fclose(file);
    printf("%s\n", name);
}

// ---- Scenes ----------------------------------------------------------------
static void reset_view(void)
{
    memset(&s_view, 0, sizeof(s_view));
    s_view.connected = true;
    s_view.sessions_ready = true;
    s_view.selection = 1;
    s_view.text_generation = 1;
    s_local_notice[0] = '\0';
    s_notice_until = 0;
    s_record_requested = s_record_dismissed = false;
    g_has_ip = true;
    g_net = MEMO_NET_CONNECTED;
    s_soc = 82;
}

static void show(agent_page_t page)
{
    build_screen(page);
    render();
}

static void session(int index, const char *title, int64_t minutes_ago)
{
    agent_session_t *item = &s_view.sessions[index];
    snprintf(item->id, sizeof(item->id), "00000000-0000-4000-8000-00000000000%d", index);
    snprintf(item->title, sizeof(item->title), "%s", title);
    item->updated_at = ((int64_t)memo_net_now() - minutes_ago * 60) * 1000;
}

static void scene_sessions(const char *out, const char *suffix)
{
    char name[64];
    reset_view();
    session(0, "明天上午的会议安排和需要准备的材料清单", 3);
    session(1, "帮我总结一下这篇关于大模型推理优化的文章", 95);
    session(2, "周末去杭州的出行计划", 60 * 26);
    session(3, "新会话", 60 * 24 * 6);
    s_view.session_count = 4;
    s_selected = 1;
    s_session_offset = 0;
    show(AGENT_PAGE_SESSIONS);
    snprintf(name, sizeof(name), "01-sessions-%s", suffix);
    save(out, name);

    static const struct { const char *name; bool ip, ready, server, loading; const char *notice; } statuses[] = {
        {"02-status-network", false, false, false, false, ""},
        {"03-status-discovering", true, false, false, false, ""},
        {"04-status-empty", true, true, true, false, ""},
        {"05-status-error", true, true, true, false, "服务器连接失败"},
    };
    for (size_t i = 0; i < sizeof(statuses) / sizeof(statuses[0]); i++) {
        reset_view();
        g_has_ip = statuses[i].ip;
        g_net = statuses[i].ip ? MEMO_NET_CONNECTED : MEMO_NET_FAILED;
        s_view.connected = statuses[i].server;
        s_view.sessions_ready = statuses[i].ready;
        snprintf(s_view.notice, sizeof(s_view.notice), "%s", statuses[i].notice);
        show(AGENT_PAGE_SESSIONS);
        snprintf(name, sizeof(name), "%s-%s", statuses[i].name, suffix);
        save(out, name);
    }
}

static void scene_chat(const char *out, const char *suffix)
{
    char name[64];
    reset_view();
    snprintf(s_view.title, sizeof(s_view.title), "%s", "大模型推理优化");
    snprintf(s_view.session_id, sizeof(s_view.session_id), "%s", "00000000-0000-4000-8000-000000000001");
    s_view.turn.present = true;
    snprintf(s_view.message.id, sizeof(s_view.message.id), "%s", "00000000-0000-4000-8000-000000000002");
    snprintf(s_view.text_id, sizeof(s_view.text_id), "%s", s_view.message.id);
    snprintf(s_view.message.state, sizeof(s_view.message.state), "%s", "completed");
    s_view.text_kind = AGENT_TEXT_REPLY;
    g_chat_text = "明天杭州 12°C～18°C，午后有雨 🌧️，记得带伞 ☂️\n① 气温：比今天低 3℃\n② 湿度 ≈ 85%，体感偏凉\n③ 空气质量良 ✅\n繁體也能顯示，人名「张喆」「王堃」也没问题 👍";
    show(AGENT_PAGE_CHAT);
    snprintf(name, sizeof(name), "06-chat-reply-%s", suffix);
    save(out, name);

    s_view.text_kind = AGENT_TEXT_INPUT;
    s_view.selection++;
    g_chat_text = "帮我总结一下这篇关于大模型推理优化的文章，重点说说哪些方法适合在端侧设备上使用。";
    render();
    snprintf(name, sizeof(name), "07-chat-input-%s", suffix);
    save(out, name);

    s_view.text_kind = AGENT_TEXT_DRAFT;
    s_view.selection++;
    g_chat_text = "再详细解释一下投机解码的原理";
    render();
    snprintf(name, sizeof(name), "08-chat-draft-%s", suffix);
    save(out, name);

    s_view.text_kind = AGENT_TEXT_REPLY;
    s_view.selection++;
    snprintf(s_view.message.state, sizeof(s_view.message.state), "%s", "waiting");
    render();
    snprintf(name, sizeof(name), "09-chat-thinking-%s", suffix);
    save(out, name);
}

static void scene_record(const char *out, const char *suffix)
{
    char name[64];
    reset_view();
    s_view.capture = AGENT_PREPARING;
    render();
    snprintf(name, sizeof(name), "10-preparing-%s", suffix);
    save(out, name);

    s_view.capture = AGENT_RECORDING;
    snprintf(s_view.text.text, sizeof(s_view.text.text), "%s", "帮我查一下明天杭州的天气，顺便提醒我带伞");
    static const uint8_t levels[] = {8, 22, 45, 70, 52, 30, 88, 64, 40, 18, 56, 34};
    for (size_t i = 0; i < sizeof(levels); i++) { g_level = levels[i]; render(); }
    snprintf(name, sizeof(name), "11-recording-%s", suffix);
    save(out, name);

    s_view.capture = AGENT_FINALIZING;
    render();
    snprintf(name, sizeof(name), "12-finalizing-%s", suffix);
    save(out, name);
}

static void scene_settings(const char *out, const char *suffix)
{
    char name[64];
    reset_view();
    s_setting_selected = MEMO_SETTING_VOLUME;
    show(AGENT_PAGE_SETTINGS);
    snprintf(name, sizeof(name), "13-settings-%s", suffix);
    save(out, name);

    memo_settings_t draft = g_settings;
    memo_ui_settings_enter(&draft);
    snprintf(name, sizeof(name), "14-settings-volume-%s", suffix);
    save(out, name);

    s_setting_selected = MEMO_SETTING_THEME;
    show(AGENT_PAGE_SETTINGS);
    draft.theme = MEMO_THEME_AUTO;
    memo_ui_settings_enter(&draft);
    snprintf(name, sizeof(name), "15-settings-theme-%s", suffix);
    save(out, name);

    g_prov = MEMO_PROV_ADVERTISING;
    show(AGENT_PAGE_PROVISION);
    snprintf(name, sizeof(name), "16-provision-%s", suffix);
    save(out, name);

    g_prov = MEMO_PROV_WIFI_CONNECTED;
    memo_ui_provision_refresh();
    snprintf(name, sizeof(name), "17-provision-done-%s", suffix);
    save(out, name);
}

int main(int argc, char **argv)
{
    const char *out = argc > 1 ? argv[1] : ".";
    lv_init();
    lv_display_t *display = lv_display_create(BSP_LCD_W, BSP_LCD_H);
    lv_display_set_color_format(display, LV_COLOR_FORMAT_RGB565);
    lv_display_set_buffers(display, g_draw, NULL, sizeof(g_draw), LV_DISPLAY_RENDER_MODE_PARTIAL);
    lv_display_set_flush_cb(display, flush);
    memo_fonts_init();
    s_transition = lv_obj_create(NULL);
    memo_ui_flat_rect(s_transition, UI_BG, 0);
    lv_screen_load(s_transition);

    static const struct { uint8_t theme; const char *suffix; } themes[] = {
        {MEMO_THEME_DARK, "dark"}, {MEMO_THEME_LIGHT, "light"},
    };
    for (size_t i = 0; i < sizeof(themes) / sizeof(themes[0]); i++) {
        g_settings.theme = themes[i].theme;
        memo_ui_apply_theme(NULL, themes[i].theme == MEMO_THEME_DARK);
        scene_sessions(out, themes[i].suffix);
        scene_chat(out, themes[i].suffix);
        scene_record(out, themes[i].suffix);
        scene_settings(out, themes[i].suffix);
    }
    return 0;
}
