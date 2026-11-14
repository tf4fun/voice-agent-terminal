#include "agent_protocol.h"

#include <math.h>
#include <string.h>
#include "cJSON.h"
#include "memo_text.h"

bool agent_valid_id(const char *id)
{
    if (id == NULL || strlen(id) != 36) return false;
    for (size_t i = 0; i < 36; i++) {
        if (i == 8 || i == 13 || i == 18 || i == 23) {
            if (id[i] != '-') return false;
        } else if (!((id[i] >= '0' && id[i] <= '9') || (id[i] >= 'a' && id[i] <= 'f'))) return false;
    }
    return true;
}

void agent_copy_text(char *out, size_t capacity, const char *input)
{
    if (capacity == 0) return;
    if (input == NULL || !memo_text_is_valid_utf8(input)) {
        out[0] = '\0';
        return;
    }
    size_t length = strlen(input);
    if (length >= capacity) {
        length = capacity - 1;
        while (length > 0 && ((unsigned char)input[length] & 0xc0) == 0x80) length--;
    }
    memcpy(out, input, length);
    out[length] = '\0';
}

static const char *string_field(const cJSON *object, const char *name)
{
    const cJSON *field = cJSON_GetObjectItemCaseSensitive(object, name);
    return cJSON_IsString(field) && field->valuestring != NULL ? field->valuestring : "";
}

static bool number_field(const cJSON *object, const char *name, uint32_t *out)
{
    const cJSON *field = cJSON_GetObjectItemCaseSensitive(object, name);
    if (!cJSON_IsNumber(field) || !isfinite(field->valuedouble) || field->valuedouble < 0 ||
        field->valuedouble > UINT32_MAX || floor(field->valuedouble) != field->valuedouble) return false;
    *out = (uint32_t)field->valuedouble;
    return true;
}

static bool object_id(const cJSON *object, char out[37])
{
    const char *id = string_field(object, "id");
    if (!agent_valid_id(id)) return false;
    memcpy(out, id, 37);
    return true;
}

static bool array_count(const cJSON *root, const cJSON **items, int *count)
{
    *items = cJSON_GetObjectItemCaseSensitive(root, "items");
    if (!cJSON_IsArray(*items)) return false;
    *count = cJSON_GetArraySize(*items);
    return *count >= 0 && *count <= AGENT_PAGE_SIZE;
}

bool agent_parse_sessions(const char *json, agent_session_t *items, int *count)
{
    cJSON *root = cJSON_Parse(json);
    const cJSON *array;
    bool valid = array_count(root, &array, count);
    for (int i = 0; valid && i < *count; i++) {
        const cJSON *item = cJSON_GetArrayItem(array, i);
        memset(&items[i], 0, sizeof(items[i]));
        valid = object_id(item, items[i].id);
        agent_copy_text(items[i].title, sizeof(items[i].title), string_field(item, "title"));
        agent_copy_text(items[i].state, sizeof(items[i].state), string_field(item, "state"));
        const cJSON *updated = cJSON_GetObjectItemCaseSensitive(item, "updated_at");
        if (cJSON_IsNumber(updated) && isfinite(updated->valuedouble) && updated->valuedouble >= 0 &&
            updated->valuedouble <= 9007199254740991.0) items[i].updated_at = (int64_t)updated->valuedouble;
    }
    cJSON_Delete(root);
    return valid;
}

bool agent_parse_draft(const char *json, agent_segment_t *items, int *count, uint32_t *revision, int *total)
{
    cJSON *root = cJSON_Parse(json);
    const cJSON *array;
    uint32_t size = 0;
    // Up to 64 draft segments and one reserved replacement operation.
    bool valid = number_field(root, "revision", revision) && number_field(root, "total", &size) &&
        size <= 65 && array_count(root, &array, count) && *count <= (int)size;
    *total = (int)size;
    for (int i = 0; valid && i < *count; i++) {
        const cJSON *item = cJSON_GetArrayItem(array, i);
        memset(&items[i], 0, sizeof(items[i]));
        valid = object_id(item, items[i].id) && number_field(item, "bytes", &items[i].bytes);
        agent_copy_text(items[i].state, sizeof(items[i].state), string_field(item, "state"));
        agent_copy_text(items[i].preview, sizeof(items[i].preview), string_field(item, "preview"));
    }
    cJSON_Delete(root);
    return valid;
}

static bool message_fields(const cJSON *root, agent_message_t *item)
{
    memset(item, 0, sizeof(*item));
    bool valid = object_id(root, item->id) && number_field(root, "revision", &item->revision) &&
        number_field(root, "input_bytes", &item->input_bytes) && number_field(root, "reply_bytes", &item->reply_bytes);
    agent_copy_text(item->state, sizeof(item->state), string_field(root, "state"));
    agent_copy_text(item->error, sizeof(item->error), string_field(root, "error"));
    valid = valid && (agent_message_active(item->state) || strcmp(item->state, "completed") == 0 ||
        strcmp(item->state, "failed") == 0 || strcmp(item->state, "incomplete") == 0 ||
        strcmp(item->state, "unconfirmed") == 0 || strcmp(item->state, "cancelled") == 0);
    return valid;
}

bool agent_parse_messages(const char *json, agent_message_t *items, int *count)
{
    cJSON *root = cJSON_Parse(json);
    const cJSON *array;
    bool valid = array_count(root, &array, count);
    for (int i = 0; valid && i < *count; i++) valid = message_fields(cJSON_GetArrayItem(array, i), &items[i]);
    cJSON_Delete(root);
    return valid;
}

bool agent_parse_message(const char *json, agent_message_t *item)
{
    cJSON *root = cJSON_Parse(json);
    bool valid = message_fields(root, item);
    cJSON_Delete(root);
    return valid;
}

bool agent_parse_text(const char *json, agent_text_t *text)
{
    cJSON *root = cJSON_Parse(json);
    const char *value = string_field(root, "text");
    bool valid = number_field(root, "offset", &text->offset) && number_field(root, "end", &text->end) &&
        number_field(root, "total", &text->total) && number_field(root, "revision", &text->revision) &&
        text->offset <= text->end && text->end <= text->total && strlen(value) <= AGENT_TEXT_BYTES &&
        text->end - text->offset == strlen(value) && memo_text_is_valid_utf8(value);
    if (valid) memcpy(text->text, value, strlen(value) + 1);
    cJSON_Delete(root);
    return valid;
}

bool agent_parse_turn(const char *json, agent_turn_t *turn)
{
    cJSON *root = cJSON_Parse(json);
    const cJSON *message = cJSON_GetObjectItemCaseSensitive(root, "message");
    memset(turn, 0, sizeof(*turn));
    const char *before = string_field(root, "before");
    const char *after = string_field(root, "after");
    bool valid = cJSON_IsObject(root) && (before[0] == '\0' || agent_valid_id(before)) &&
        (after[0] == '\0' || agent_valid_id(after));
    turn->present = cJSON_IsObject(message);
    if (turn->present) valid = valid && message_fields(message, &turn->message);
    else valid = valid && cJSON_IsNull(message);
    if (valid) {
        agent_copy_text(turn->before, sizeof(turn->before), before);
        agent_copy_text(turn->after, sizeof(turn->after), after);
    }
    cJSON_Delete(root);
    return valid;
}

bool agent_parse_revision(const char *json, uint32_t *revision)
{
    cJSON *root = cJSON_Parse(json);
    bool valid = number_field(root, "revision", revision);
    cJSON_Delete(root);
    return valid;
}

bool agent_message_active(const char *state)
{
    return strcmp(state, "queued") == 0 || strcmp(state, "sending") == 0 ||
        strcmp(state, "waiting") == 0 || strcmp(state, "receiving") == 0 || strcmp(state, "attention") == 0;
}


