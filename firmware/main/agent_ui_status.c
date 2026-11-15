#include "agent_ui_status.h"

#include <string.h>
#include "memo_font.h"
#include "memo_ui_common.h"

static lv_obj_t *s_panel, *s_icon, *s_title, *s_detail;
static agent_status_t s_status = AGENT_STATUS_NEEDS_NETWORK;

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

static void outline(lv_layer_t *layer, int x, int y, int width, int height, int radius)
{
    lv_draw_rect_dsc_t rect;
    lv_draw_rect_dsc_init(&rect);
    rect.bg_opa = LV_OPA_TRANSP;
    rect.border_color = lv_color_hex(UI_ACCENT);
    rect.border_width = 2;
    rect.radius = radius;
    const lv_area_t area = {x, y, x + width - 1, y + height - 1};
    lv_draw_rect(layer, &rect, &area);
}

static void arc(lv_layer_t *layer, int cx, int cy, int radius, int start, int end)
{
    lv_draw_arc_dsc_t dsc;
    lv_draw_arc_dsc_init(&dsc);
    dsc.color = lv_color_hex(UI_ACCENT);
    dsc.width = 2;
    dsc.rounded = 1;
    dsc.center = (lv_point_t){cx, cy};
    dsc.radius = radius;
    dsc.start_angle = start;
    dsc.end_angle = end;
    lv_draw_arc(layer, &dsc);
}

static void dot(lv_layer_t *layer, int cx, int cy)
{
    lv_draw_rect_dsc_t rect;
    lv_draw_rect_dsc_init(&rect);
    rect.bg_color = lv_color_hex(UI_ACCENT);
    rect.radius = LV_RADIUS_CIRCLE;
    const lv_area_t area = {cx - 2, cy - 2, cx + 2, cy + 2};
    lv_draw_rect(layer, &rect, &area);
}

// Icons are drawn around the centre of the circular accent badge.
static void draw_icon(lv_event_t *event)
{
    lv_area_t area;
    lv_obj_get_coords(lv_event_get_target_obj(event), &area);
    lv_layer_t *layer = lv_event_get_layer(event);
    const int cx = (area.x1 + area.x2) / 2, cy = (area.y1 + area.y2) / 2;
    if (s_status == AGENT_STATUS_NEEDS_NETWORK || s_status == AGENT_STATUS_CONNECTING) {
        for (int radius = 7; radius <= 21; radius += 7) arc(layer, cx, cy + 9, radius, 225, 315);
        dot(layer, cx, cy + 9);
        if (s_status == AGENT_STATUS_NEEDS_NETWORK) stroke(layer, cx - 13, cy + 13, cx + 13, cy - 13);
    } else if (s_status == AGENT_STATUS_EMPTY) {
        outline(layer, cx - 6, cy - 16, 12, 21, 6);
        arc(layer, cx, cy - 3, 11, 0, 180);
        stroke(layer, cx, cy + 8, cx, cy + 13);
        stroke(layer, cx - 6, cy + 14, cx + 6, cy + 14);
    } else if (s_status == AGENT_STATUS_ERROR) {
        arc(layer, cx, cy, 15, 0, 360);
        stroke(layer, cx, cy - 7, cx, cy + 2);
        dot(layer, cx, cy + 8);
    } else {
        for (int row = 0; row < 2; row++) {
            const int top = cy - 13 + row * 15;
            outline(layer, cx - 14, top, 28, 11, 4);
            dot(layer, cx - 8, top + 5);
            stroke(layer, cx - 1, top + 5, cx + 8, top + 5);
        }
    }
}

static void text(lv_obj_t *label, const char *value)
{
    if (strcmp(lv_label_get_text(label), value) != 0) memo_font_label_text(label, value, false);
}

void agent_ui_status_build(lv_obj_t *parent)
{
    if (s_panel != NULL) return;
    const agent_status_layout_t layout = agent_status_layout(lv_display_get_horizontal_resolution(NULL),
                                                            lv_display_get_vertical_resolution(NULL));
    s_panel = lv_obj_create(parent);
    memo_ui_flat_rect(s_panel, UI_BG, 0);
    lv_obj_set_pos(s_panel, layout.x, layout.y);
    lv_obj_set_size(s_panel, layout.width, layout.height);
    s_icon = memo_ui_icon_badge(s_panel, layout.icon_x, layout.icon_y, layout.icon_size);
    lv_obj_add_event_cb(s_icon, draw_icon, LV_EVENT_DRAW_MAIN, NULL);
    s_title = memo_ui_label(s_panel, memo_font_body(), UI_FG);
    lv_obj_set_pos(s_title, 12, layout.title_y);
    lv_obj_set_width(s_title, layout.width - 24);
    lv_obj_set_style_text_align(s_title, LV_TEXT_ALIGN_CENTER, 0);
    s_detail = memo_ui_label(s_panel, memo_font_label(), UI_DIM);
    lv_obj_set_pos(s_detail, 12, layout.detail_y);
    lv_obj_set_width(s_detail, layout.width - 24);
    lv_obj_set_height(s_detail, 2 * memo_font_label()->line_height);
    lv_label_set_long_mode(s_detail, LV_LABEL_LONG_DOT);
    lv_obj_set_style_text_align(s_detail, LV_TEXT_ALIGN_CENTER, 0);
}

void agent_ui_status_refresh(agent_status_t status, const char *notice)
{
    if (s_panel == NULL) return;
    if (s_status != status) {
        s_status = status;
        lv_obj_invalidate(s_icon);
    }
    const char *title, *detail;
    switch (status) {
    case AGENT_STATUS_NEEDS_NETWORK:
        title = "连接网络"; detail = "长按上键切换到设置"; break;
    case AGENT_STATUS_CONNECTING:
        title = "正在连接网络"; detail = "连接已保存的网络"; break;
    case AGENT_STATUS_DISCOVERING:
        title = "正在查找服务器"; detail = "自动发现局域网服务"; break;
    case AGENT_STATUS_SYNCING:
        title = "读取会话"; detail = "正在同步会话列表"; break;
    case AGENT_STATUS_EMPTY:
        title = "开启新会话"; detail = "长按确认键开始录音"; break;
    default:
        title = "连接遇到问题"; detail = notice != NULL ? notice : "请稍后重试"; break;
    }
    text(s_title, title);
    text(s_detail, detail);
}

void agent_ui_status_destroy(void)
{
    if (s_panel != NULL) lv_obj_delete(s_panel);
    s_panel = s_icon = s_title = s_detail = NULL;
}
