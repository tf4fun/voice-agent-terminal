#include "memo_ui_provision.h"

#include <stdio.h>
#include <string.h>

#include "memo_font.h"
#include "memo_provision.h"
#include "memo_provision_layout.h"
#include "memo_ui_common.h"

static lv_obj_t *s_screen, *s_icon, *s_state, *s_detail;
static lv_obj_t *s_identity_card, *s_identity, *s_footer;
static memo_provision_state_t s_visual_state = MEMO_PROV_IDLE;

static void text(lv_obj_t *label, const char *value)
{
    if (strcmp(lv_label_get_text(label), value) != 0)
        memo_font_label_text(label, value, false);
}

static void stroke(lv_layer_t *layer, int x1, int y1, int x2, int y2)
{
    lv_draw_line_dsc_t line;
    lv_draw_line_dsc_init(&line);
    line.color = lv_color_hex(UI_ACCENT);
    line.width = 2;
    line.round_start = line.round_end = 1;
    line.p1 = (lv_point_precise_t){x1, y1};
    line.p2 = (lv_point_precise_t){x2, y2};
    lv_draw_line(layer, &line);
}

// Icons are drawn around the centre of the circular accent badge.
static void draw_icon(lv_event_t *event)
{
    lv_area_t area;
    lv_obj_get_coords(lv_event_get_target_obj(event), &area);
    lv_layer_t *layer = lv_event_get_layer(event);
    const int cx = (area.x1 + area.x2) / 2, cy = (area.y1 + area.y2) / 2;
    if (s_visual_state == MEMO_PROV_WIFI_CONNECTED) {
        stroke(layer, cx - 10, cy + 1, cx - 3, cy + 8);
        stroke(layer, cx - 3, cy + 8, cx + 11, cy - 7);
    } else if (s_visual_state == MEMO_PROV_FAILED) {
        stroke(layer, cx - 8, cy - 8, cx + 8, cy + 8);
        stroke(layer, cx + 8, cy - 8, cx - 8, cy + 8);
    } else {
        // Bluetooth mark uses drawing primitives, independent of font coverage.
        stroke(layer, cx, cy - 14, cx, cy + 14);
        stroke(layer, cx, cy - 14, cx + 8, cy - 6);
        stroke(layer, cx + 8, cy - 6, cx - 8, cy + 7);
        stroke(layer, cx - 8, cy - 7, cx + 8, cy + 6);
        stroke(layer, cx + 8, cy + 6, cx, cy + 14);
    }
}

static lv_obj_t *centered_label(lv_obj_t *parent, int x, int y, int width,
                                 int height, uint32_t color)
{
    lv_obj_t *label = memo_ui_label(parent, memo_font_label(), color);
    lv_obj_set_pos(label, x, y);
    lv_obj_set_size(label, width, height);
    lv_obj_set_style_text_align(label, LV_TEXT_ALIGN_CENTER, 0);
    lv_label_set_long_mode(label, LV_LABEL_LONG_DOT);
    return label;
}

void memo_ui_provision_build(void)
{
    if (s_screen != NULL) return;
    const int width = lv_display_get_horizontal_resolution(NULL);
    const int height = lv_display_get_vertical_resolution(NULL);
    const int line_height = memo_font_label()->line_height;
    const memo_provision_layout_t layout = memo_provision_layout(width, height, line_height);

    s_screen = lv_obj_create(NULL);
    memo_ui_flat_rect(s_screen, UI_BG, 0);
    memo_ui_header(s_screen, "蓝牙配网", true);
    s_icon = memo_ui_icon_badge(s_screen, layout.icon_x, layout.icon_y, layout.icon_size);
    lv_obj_add_event_cb(s_icon, draw_icon, LV_EVENT_DRAW_MAIN, NULL);
    s_state = centered_label(s_screen, 16, layout.title_y, width - 32, line_height, UI_FG);
    lv_obj_set_style_text_font(s_state, memo_font_body(), 0);
    s_detail = centered_label(s_screen, layout.text_x, layout.detail_y,
        layout.text_width, layout.detail_height, UI_DIM);
    lv_label_set_long_mode(s_detail, LV_LABEL_LONG_WRAP);
    lv_obj_set_style_text_line_space(s_detail, MEMO_PROVISION_LINE_SPACE, 0);
    s_identity_card = memo_ui_card(s_screen, layout.text_x, layout.identity_y,
        layout.text_width, layout.identity_height, NULL);
    lv_obj_set_style_radius(s_identity_card, layout.identity_height / 2, 0);
    lv_obj_set_style_pad_all(s_identity_card, 0, 0);
    s_identity = centered_label(s_identity_card, 12, 5, layout.text_width - 26,
        line_height, UI_FG);
    lv_label_set_long_mode(s_identity, LV_LABEL_LONG_SCROLL_CIRCULAR);
    s_footer = centered_label(s_screen, layout.text_x, layout.footer_y,
        layout.text_width, layout.footer_height, UI_DIM);
    memo_ui_provision_refresh();
}

void memo_ui_provision_destroy(void)
{
    memo_ui_screen_delete(s_screen);
    s_screen = s_icon = s_state = s_detail = NULL;
    s_identity_card = s_identity = s_footer = NULL;
    s_visual_state = MEMO_PROV_IDLE;
}

void memo_ui_provision_refresh(void)
{
    if (s_screen == NULL) return;
    const memo_provision_state_t state = memo_provision_state();
    if (s_visual_state != state) {
        s_visual_state = state;
        lv_obj_invalidate(s_icon);
    }
    memo_network_info_t info;
    memo_provision_info(&info);
    const char *title, *detail, *footer = "";
    char identity[64] = "";
    char address[32];
    switch (state) {
    case MEMO_PROV_ADVERTISING:
        title = "等待手机连接";
        detail = "打开微信配网小程序\n选择下方设备";
        snprintf(identity, sizeof(identity), "%s", MEMO_PROV_DEVICE_NAME);
        footer = "请开启手机蓝牙";
        break;
    case MEMO_PROV_BLE_CONNECTED:
        title = "手机已连接";
        detail = "在手机中选择网络\n并输入无线网络密码";
        snprintf(identity, sizeof(identity), "%s", MEMO_PROV_DEVICE_NAME);
        footer = "仅支持 2.4 GHz Wi-Fi";
        break;
    case MEMO_PROV_WIFI_CONNECTING:
        title = "正在连接网络";
        detail = "正在连接无线网络\n请稍候";
        snprintf(identity, sizeof(identity), "%s", info.ssid[0] ? info.ssid : "无线网络");
        footer = "仅支持 2.4 GHz Wi-Fi";
        break;
    case MEMO_PROV_WIFI_CONNECTED:
        title = "配网完成";
        detail = "网络已连接\n确认后返回设置";
        snprintf(identity, sizeof(identity), "%s", info.ssid[0] ? info.ssid : "无线网络");
        snprintf(address, sizeof(address), "IP %s", info.ip[0] ? info.ip : "--");
        footer = address;
        break;
    case MEMO_PROV_FAILED:
        title = "配网失败";
        detail = "请返回设置\n重新开启配网";
        footer = esp_err_to_name(memo_provision_error());
        break;
    default:
        title = "准备配网";
        detail = "正在启动蓝牙\n请稍候";
        break;
    }
    // SSIDs can contain control bytes. Keep the identifier in one visual line.
    for (char *character = identity; *character != '\0'; character++)
        if ((unsigned char)*character < 32) *character = ' ';
    text(s_state, title);
    text(s_detail, detail);
    text(s_identity, identity);
    lv_obj_set_hidden(s_identity_card, identity[0] == '\0');
    text(s_footer, footer);
    lv_obj_set_hidden(s_footer, footer[0] == '\0');
}

lv_obj_t *memo_ui_provision_screen(void)
{
    return s_screen;
}
