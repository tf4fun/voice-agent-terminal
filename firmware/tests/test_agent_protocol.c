#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "agent_logic.h"
#include "agent_chat_layout.h"
#include "agent_status.h"
#include "agent_protocol.h"
#include "memo_key.h"
#include "memo_browse.h"
#include "memo_provision_layout.h"

static const char *id = "00000000-0000-4000-8000-000000000001";

static void test_bounded_protocol(void)
{
    agent_session_t sessions[4];
    int count = -1;
    char json[4096];
    snprintf(json, sizeof(json), "{\"items\":[{\"id\":\"%s\",\"title\":\"中文标题\",\"state\":\"ready\",\"updated_at\":1794561389602}]}", id);
    assert(agent_parse_sessions(json, sessions, &count));
    assert(count == 1 && strcmp(sessions[0].title, "中文标题") == 0);
    assert(sessions[0].updated_at == 1794561389602LL);
    assert(!agent_parse_sessions("{\"items\":[{\"id\":\"wrong\"}]}", sessions, &count));
    assert(!agent_parse_sessions("{\"items\":[{},{},{},{},{}]}", sessions, &count));
    assert(!agent_parse_sessions("null", sessions, &count));
    assert(!agent_parse_sessions("[]", sessions, &count));
    char title[8];
    agent_copy_text(title, sizeof(title), "中文标题");
    assert(strcmp(title, "中文") == 0);
    agent_copy_text(title, sizeof(title), "\xff");
    assert(title[0] == '\0');
    agent_segment_t segments[4];
    uint32_t revision = 0;
    int total = 0;
    snprintf(json, sizeof(json), "{\"revision\":25,\"total\":1,\"items\":[{\"id\":\"%s\",\"state\":\"ready\",\"preview\":\"第一段\",\"bytes\":9}]}", id);
    assert(agent_parse_draft(json, segments, &count, &revision, &total));
    assert(revision == 25 && count == 1 && segments[0].bytes == 9);
    assert(agent_parse_draft("{\"revision\":1,\"total\":65,\"items\":[]}", segments, &count, &revision, &total));
    assert(total == 65);
    assert(!agent_parse_draft("{\"revision\":1,\"total\":66,\"items\":[]}", segments, &count, &revision, &total));
    assert(!agent_parse_draft("{\"revision\":-1,\"items\":[]}", segments, &count, &revision, &total));
    assert(!agent_parse_revision("{\"revision\":4294967296}", &revision));
    assert(!agent_parse_revision("{\"revision\":1.5}", &revision));
    agent_text_t text;
    assert(agent_parse_text("{\"text\":\"你好\",\"offset\":3,\"end\":9,\"total\":12,\"revision\":4}", &text));
    assert(strcmp(text.text, "你好") == 0 && text.end == 9);
    assert(!agent_parse_text("{\"text\":\"你好\",\"offset\":3,\"end\":8,\"total\":12,\"revision\":4}", &text));
    assert(!agent_parse_text("{\"text\":\"你好\",\"offset\":3,\"end\":9,\"total\":8,\"revision\":4}", &text));
    char large[514];
    memset(large, 'a', sizeof(large) - 1); large[sizeof(large) - 1] = '\0';
    snprintf(json, sizeof(json), "{\"text\":\"%s\",\"offset\":0,\"end\":513,\"total\":513,\"revision\":1}", large);
    assert(!agent_parse_text(json, &text));
    agent_message_t message;
    snprintf(json, sizeof(json), "{\"id\":\"%s\",\"state\":\"completed\",\"revision\":3,\"input_bytes\":12,\"reply_bytes\":18}", id);
    assert(agent_parse_message(json, &message));
    assert(!agent_message_active(message.state));
    assert(agent_message_active("receiving") && agent_message_active("attention"));
    assert(!agent_message_active("unconfirmed"));
}

static void test_navigation_and_gestures(void)
{
    const memo_rows_layout_t rows = memo_rows_layout(240, 320);
    assert(rows.rows == 4 && rows.row_height >= 33 + 20);
    assert(rows.top + 3 * rows.pitch + rows.row_height <= 320 - 12);
    assert(rows.x + 12 == 240 - (rows.x + rows.width - 12));
    const memo_rows_layout_t settings = memo_settings_layout(240, 320);
    assert(settings.rows == 4 && settings.top + 3 * settings.pitch + settings.row_height <= 320 - 12);
    agent_step_t step = agent_page_step(3, 4, 1, false, true);
    assert(step.selected == 3 && step.direction == 1);
    step = agent_page_step(3, 4, 1, false, false);
    assert(step.selected == 3 && step.direction == 0);
    step = agent_page_step(0, 4, -1, true, false);
    assert(step.selected == 0 && step.direction == -1);
    step = agent_page_step(0, 0, -1, true, false);
    assert(step.selected == 0 && step.direction == -1);
    assert(agent_page_exit(AGENT_PAGE_SETTINGS, true) == AGENT_PAGE_SETTINGS);
    assert(agent_page_exit(AGENT_PAGE_SETTINGS, false) == AGENT_PAGE_SESSIONS);
    assert(agent_page_exit(AGENT_PAGE_CHAT, false) == AGENT_PAGE_SESSIONS);
    assert(agent_page_exit(AGENT_PAGE_PROVISION, false) == AGENT_PAGE_SETTINGS);
    assert(agent_page_exit(AGENT_PAGE_SESSIONS, false) == AGENT_PAGE_SESSIONS);
    assert(agent_page_exit(AGENT_PAGE_PREPARING, false) == AGENT_PAGE_SESSIONS);
    assert(agent_page_exit(AGENT_PAGE_RECORD, false) == AGENT_PAGE_SESSIONS);
    assert(agent_page_can_record(AGENT_PAGE_SESSIONS));
    assert(agent_page_can_record(AGENT_PAGE_CHAT));
    assert(!agent_page_can_record(AGENT_PAGE_SETTINGS));
    assert(agent_sleep_ready(true, false, true));
    assert(!agent_sleep_ready(true, false, false));
    assert(!agent_sleep_ready(false, false, true));
    assert(!agent_sleep_ready(true, true, true));
    assert(agent_page_is_root(AGENT_PAGE_SESSIONS, false));
    assert(agent_page_is_root(AGENT_PAGE_SETTINGS, false));
    assert(!agent_page_is_root(AGENT_PAGE_SETTINGS, true));
    assert(!agent_page_is_root(AGENT_PAGE_CHAT, false));
    assert(agent_page_cycle(AGENT_PAGE_SESSIONS, agent_root_pages, AGENT_ROOT_PAGE_COUNT) == AGENT_PAGE_SETTINGS);
    assert(agent_page_cycle(AGENT_PAGE_SETTINGS, agent_root_pages, AGENT_ROOT_PAGE_COUNT) == AGENT_PAGE_SESSIONS);
    const agent_page_t three_roots[] = {AGENT_PAGE_SESSIONS, AGENT_PAGE_SETTINGS, AGENT_PAGE_CHAT};
    assert(agent_page_cycle(AGENT_PAGE_SETTINGS, three_roots, 3) == AGENT_PAGE_CHAT);
    assert(agent_page_cycle(AGENT_PAGE_CHAT, three_roots, 3) == AGENT_PAGE_SESSIONS);
    memo_key_t key = {0};
    for (int i = 0; i < 8; i++) {
        memo_key_press(&key, i * 100000, true);
        assert(memo_key_release(&key, i * 100000 + 20000, 500000) == MEMO_KEY_CLICK);
    }
    memo_key_press(&key, 1000000, true);
    assert(memo_key_hold(&key, 1500000, 500000, false) == MEMO_KEY_HOLD);
    assert(memo_key_release(&key, 1510000, 500000) == MEMO_KEY_NONE);
    assert(memo_key_hold(&key, 1600000, 500000, true) == MEMO_KEY_NONE);
    agent_page_t page = AGENT_PAGE_SETTINGS;
    bool editing = true;
    for (int press = 0; press < 4; press++) {
        const int64_t start = 2000000 + press * 1000000;
        memo_key_press(&key, start, true);
        assert(memo_key_hold(&key, start + 500000, 500000, false) == MEMO_KEY_HOLD);
        page = agent_page_exit(page, editing);
        editing = false;
        assert(page == (press == 0 ? AGENT_PAGE_SETTINGS : AGENT_PAGE_SESSIONS));
        assert(memo_key_hold(&key, start + 600000, 500000, true) == MEMO_KEY_NONE);
        assert(memo_key_release(&key, start + 700000, 500000) == MEMO_KEY_NONE);
    }
}

static void test_session_status(void)
{
    assert(agent_session_status(false, false, false, false, false, false) == AGENT_STATUS_NEEDS_NETWORK);
    assert(agent_session_status(false, true, false, false, false, false) == AGENT_STATUS_CONNECTING);
    assert(agent_session_status(true, false, false, false, false, false) == AGENT_STATUS_DISCOVERING);
    assert(agent_session_status(true, false, true, false, false, false) == AGENT_STATUS_SYNCING);
    assert(agent_session_status(true, false, true, true, true, false) == AGENT_STATUS_SYNCING);
    assert(agent_session_status(true, false, true, true, false, false) == AGENT_STATUS_EMPTY);
    assert(agent_session_status(true, false, false, false, false, true) == AGENT_STATUS_ERROR);
    assert(agent_session_status(false, true, false, true, false, true) == AGENT_STATUS_CONNECTING);
    const agent_status_layout_t layout = agent_status_layout(240, 320);
    assert(layout.x + layout.width / 2 == 120);
    assert(layout.x + layout.icon_x + layout.icon_size / 2 == 120);
    assert(layout.y >= 52 && layout.y + layout.height <= 296);
    assert(layout.icon_y + layout.icon_size <= layout.title_y);
    assert(layout.title_y + 31 <= layout.detail_y);
    assert(layout.detail_y + 2 * 31 <= layout.height);
}

static void test_whole_turn(void)
{
    agent_turn_t turn;
    assert(agent_parse_turn("{\"message\":null,\"before\":\"\",\"after\":\"\"}", &turn));
    assert(!turn.present);
    assert(agent_parse_turn("{\"message\":{\"id\":\"00000000-0000-4000-8000-000000000001\",\"state\":\"waiting\",\"revision\":3,\"input_bytes\":12000,\"reply_bytes\":0},\"before\":\"\",\"after\":\"\"}", &turn));
    assert(turn.present && turn.message.input_bytes == 12000);
    assert(!agent_parse_turn("{\"message\":{\"id\":\"00000000-0000-4000-8000-000000000001\",\"revision\":3,\"input_bytes\":1,\"reply_bytes\":0}}", &turn));
    assert(!agent_parse_turn("{\"message\":null,\"before\":\"bad-id\"}", &turn));
}

static void test_chat_layout(void)
{
    const agent_chat_card_layout_t card = agent_chat_card_layout(240, 320, 31);
    assert(card.x == 12 && card.width == 216 && card.top == 34);
    assert(card.top + card.height == 308);
    assert(card.body_width == 190);
    assert(card.body_y == 32 && card.body_height == 228);
    assert(card.role_y + 31 < card.body_y);
    assert(card.body_y + card.body_height == card.height - 14);
    assert(card.status_y == card.role_y && card.status_x >= 12 + 40);
    assert(card.status_y + 31 < card.body_y);
    assert(card.status_x + card.status_width == 12 + card.body_width);
    for (int back = 0; back <= 1; back++) {
        const memo_header_layout_t header = memo_header_layout(240, back);
        assert(header.clock_x + header.clock_width <= header.title_x);
        assert(header.title_x + header.title_width <= header.battery_x);
        assert(header.title_x + header.title_width / 2 == 120);
    }
}

static void test_provision_layout(void)
{
    const memo_provision_layout_t layout = memo_provision_layout(240, 320, 31);
    assert(layout.icon_x + layout.icon_size / 2 == 120);
    assert(layout.text_x + layout.text_width / 2 == 120);
    assert(layout.icon_y >= 52 && layout.icon_y + layout.icon_size < layout.title_y);
    assert(layout.title_y + 31 < layout.detail_y);
    assert(layout.detail_y + layout.detail_height < layout.identity_y);
    assert(layout.identity_y + layout.identity_height <= layout.footer_y);
    assert(layout.footer_y + layout.footer_height <= 320 - 12);
    assert(layout.text_width - 26 > 160);
}

int main(void)
{
    assert(agent_valid_id(id));
    test_bounded_protocol();
    test_session_status();
    test_chat_layout();
    test_provision_layout();
    test_whole_turn();
    test_navigation_and_gestures();
    puts("PASS: bounded device JSON, UTF-8 windows, four-row paging and per-press gestures");
    return 0;
}
