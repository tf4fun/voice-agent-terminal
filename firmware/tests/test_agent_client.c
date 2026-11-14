// Client state and transport regressions. Include the complete production translation unit so changes
// to guards, command publication, request construction and response handling run
// through their actual entry points. All I/O and scheduling are deterministic.
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#ifndef AGENT_CLIENT_SOURCE
#define AGENT_CLIENT_SOURCE "../main/agent_client.c"
#endif
#include AGENT_CLIENT_SOURCE

static const char *session_id = "00000000-0000-4000-8000-000000000001";
static unsigned failed;
#define CHECK(condition) do { if (!(condition)) { \
    fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #condition); failed++; \
} } while (0)

struct fake_queue {
    unsigned count, capacity;
    size_t size;
    unsigned char items[6][1024];
};
static struct fake_queue queue_fixture;
struct fake_http {
    char url[384], response[32768];
    esp_err_t (*event_handler)(esp_http_client_event_t *event);
    void *user_data;
    size_t length, read;
    int status;
    esp_http_client_method_t method;
    char request_body[128];
    size_t written;
};
static int response_status = 200;
static int stream_timeout_ms;
static const char *injected_response;
static const char *whole_input, *whole_reply, *whole_draft;
static int declared_extra;
static const char *message_id = "00000000-0000-4000-8000-000000000002";
static const char *selected_message_id = "00000000-0000-4000-8000-000000000004";
static const char retry_response[] =
    "{\"id\":\"00000000-0000-4000-8000-000000000002\",\"state\":\"queued\","
    "\"revision\":4,\"input_bytes\":1,\"reply_bytes\":9}";
static void (*response_hook)(void);

// Capture scheduling hooks are deterministic points in actual upload_audio().
enum capture_case {
    CAPTURE_NORMAL, CAPTURE_CANCEL_RECORDING, CAPTURE_CANCEL_FINALIZING,
    CAPTURE_CANCEL_BEFORE_COMMIT, CAPTURE_EXIT_COMMITTING,
    CAPTURE_LOST_ACK_ONCE, CAPTURE_LOST_ACK_TWICE, CAPTURE_SHORT,
};
static bool capture_mode, capture_running, upload_closed, stage_ready, stage_exists;
static bool server_committed, cancel_accepted, audio_eof;
static unsigned chunks_read, cancel_attempts, commit_requests, commit_applies, abort_requests;
static enum capture_case capture_case;
static char server_draft;
static void try_cancel(void)
{
    cancel_attempts++;
    cancel_accepted = agent_client_cancel_record();
}

QueueHandle_t xQueueCreate(unsigned count, size_t size)
{
    assert(count <= 6 && size <= sizeof(queue_fixture.items[0]));
    queue_fixture = (struct fake_queue){.capacity=count, .size=size};
    return &queue_fixture;
}
int xQueueSend(QueueHandle_t handle, const void *item, unsigned timeout)
{
    (void)timeout;
    struct fake_queue *queue = handle;
    if (queue->count == queue->capacity) return pdFALSE;
    memcpy(queue->items[queue->count++], item, queue->size);
    return pdTRUE;
}
int xQueueReceive(QueueHandle_t handle, void *item, unsigned timeout)
{
    (void)timeout;
    struct fake_queue *queue = handle;
    if (!queue->count) return pdFALSE;
    memcpy(item, queue->items[0], queue->size);
    queue->count--;
    memmove(queue->items[0], queue->items[1], queue->count * sizeof(queue->items[0]));
    return pdTRUE;
}
unsigned uxQueueMessagesWaiting(QueueHandle_t handle) { return ((struct fake_queue *)handle)->count; }
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (void *)1; }
int xSemaphoreTake(SemaphoreHandle_t handle, unsigned timeout)
{
    (void)handle;(void)timeout;
    if(capture_mode && capture_case==CAPTURE_CANCEL_BEFORE_COMMIT &&
       upload_closed && s_view.capture==AGENT_FINALIZING && cancel_attempts==0) try_cancel();
    return pdTRUE;
}
int xSemaphoreGive(SemaphoreHandle_t handle)
{
    (void)handle;
    if(capture_mode && capture_case==CAPTURE_EXIT_COMMITTING &&
       s_view.capture==AGENT_COMMITTING && cancel_attempts==0) try_cancel();
    return pdTRUE;
}
int xTaskCreate(void (*callback)(void *), const char *name, unsigned stack,
                void *argument, unsigned priority, TaskHandle_t *task)
{
    (void)callback;(void)name;(void)stack;(void)argument;(void)priority;
    if (task) *task = (void *)1;
    return pdPASS;
}
void vTaskDelay(unsigned ticks) { (void)ticks; }
unsigned ulTaskNotifyTake(int clear, unsigned timeout) { (void)clear;(void)timeout;return 0; }
void xTaskNotifyGive(TaskHandle_t task) { (void)task; }
int64_t esp_timer_get_time(void) { static int64_t time;return time += 1000; }
void esp_fill_random(void *data, size_t size) { memset(data, 1, size); }
bool memo_net_has_ip(void) { return true; }
esp_err_t memo_net_start(void) { return ESP_OK; }
esp_err_t memo_net_suspend(void) { return ESP_OK; }
esp_err_t memo_provision_start(void) { return ESP_OK; }
esp_err_t memo_provision_stop(void) { return ESP_OK; }
esp_err_t memo_discovery_init(void) { return ESP_OK; }
esp_err_t memo_discovery_search(uint32_t timeout) { (void)timeout;return ESP_OK; }
void memo_discovery_stop(void) {}
bool memo_discovery_endpoint(memo_endpoint_t *endpoint)
{
    *endpoint = (memo_endpoint_t){.host="review.invalid", .port=8080, .generation=1, .confirmed=true};
    return true;
}
void memo_discovery_confirm(const memo_endpoint_t *endpoint) { (void)endpoint; }
void memo_discovery_invalidate(const memo_endpoint_t *endpoint) { (void)endpoint; }
esp_err_t agent_capture_begin(void) { capture_running=true;return ESP_OK; }
void agent_capture_end(void) { capture_running=false; }
bool agent_capture_running(void) { return capture_running; }
esp_err_t agent_capture_error(void) { return ESP_OK; }
size_t agent_capture_read(void *buffer, size_t capacity, uint32_t timeout)
{
    (void)buffer;(void)capacity;(void)timeout;
    unsigned count=capture_case==CAPTURE_SHORT?1:5;
    if(chunks_read++<count)return 640;
    if(capture_case==CAPTURE_CANCEL_RECORDING) try_cancel();
    else agent_client_release_record();
    return 0;
}
uint32_t agent_capture_duration_ms(void) { return chunks_read*20; }

esp_http_client_handle_t esp_http_client_init(const esp_http_client_config_t *config)
{
    struct fake_http *client=calloc(1,sizeof(*client)); assert(client);
    snprintf(client->url,sizeof(client->url),"%s",config->url);
    client->method=config->method;
    client->event_handler=config->event_handler;client->user_data=config->user_data;
    if(capture_mode && strstr(client->url,"/audio?")) {
        CHECK(strstr(client->url,"defer_commit=1")!=NULL);
        stage_exists=true;
    }
    return client;
}
static void emit_content_headers(esp_http_client_handle_t client)
{
    if(client->event_handler==NULL)return;
    esp_http_client_event_t event={.event_id=HTTP_EVENT_ON_HEADER,.user_data=client->user_data,
        .header_key="X-Revision",.header_value="8"};
    client->event_handler(&event);
    event.header_key="X-Message-State";event.header_value="completed";
    client->event_handler(&event);
}

esp_err_t esp_http_client_set_header(esp_http_client_handle_t client,const char *key,const char *value)
{ (void)client;(void)key;(void)value;return ESP_OK; }
esp_err_t esp_http_client_open(esp_http_client_handle_t client,int size)
{ (void)client;(void)size;return ESP_OK; }
int esp_http_client_write(esp_http_client_handle_t client,const char *data,int size)
{
    if(capture_mode && strstr(client->url,"/audio?")) {
        if(size==5 && memcmp(data,"0\r\n\r\n",5)==0)audio_eof=true;
    } else if(capture_mode) {
        assert(client->written+(size_t)size<sizeof(client->request_body));
        memcpy(client->request_body+client->written,data,(size_t)size);
        client->written+=(size_t)size;
        client->request_body[client->written]=0;
    }
    return size;
}
int64_t esp_http_client_fetch_headers(esp_http_client_handle_t client)
{
    client->status=response_status;
    if(capture_mode) {
        client->status=200;
        if(strstr(client->url,"/audio?")) {
            CHECK(audio_eof);
            CHECK(s_view.capture==AGENT_FINALIZING);
            stage_ready=true;
            strcpy(client->response,"{\"revision\":7}");
            if(capture_case==CAPTURE_CANCEL_FINALIZING)try_cancel();
        } else if(strstr(client->url,"/commit")) {
            CHECK(s_view.capture==AGENT_COMMITTING);
            CHECK(strcmp(client->request_body,"{\"revision\":7}")==0);
            commit_requests++;
            if(!server_committed) {
                CHECK(stage_ready);
                server_committed=true;server_draft='B';commit_applies++;
                stage_exists=stage_ready=false;
            }
            unsigned lost=0;
            if(capture_case==CAPTURE_LOST_ACK_ONCE)lost=1;
            if(capture_case==CAPTURE_LOST_ACK_TWICE)lost=2;
            if(commit_requests<=lost)return -1;
            strcpy(client->response,"{\"revision\":8}");
        } else if(strstr(client->url,"/abort")) {
            abort_requests++;
            if(server_committed)strcpy(client->response,"{\"status\":\"committed\"}");
            else {
                stage_exists=stage_ready=false;
                strcpy(client->response,"{\"status\":\"cancelled\"}");
            }
        } else {
            CHECK(false);strcpy(client->response,"{}");
        }
        client->length=strlen(client->response);
        return (int64_t)client->length;
    }
    if (strstr(client->url,"/content") != NULL) {
        const char *value=strstr(client->url,"part=input")?whole_input:
            strstr(client->url,"part=reply")?whole_reply:whole_draft;
        snprintf(client->response,sizeof(client->response),"%s",value==NULL?"":value);
        emit_content_headers(client);
    } else if (injected_response != NULL) {
        snprintf(client->response, sizeof(client->response), "%s", injected_response);
    } else if(response_status==200) {
        bool before=strstr(client->url,"before=512")!=NULL;
        const char *offset_value=strstr(client->url,"offset=");
        int offset=before ? 0 : offset_value ? atoi(offset_value+7) : 0;
        char text[513];memset(text,offset==0?'A':'B',512);text[512]=0;
        snprintf(client->response,sizeof(client->response),
            "{\"text\":\"%s\",\"offset\":%d,\"end\":%d,\"total\":1536,\"revision\":1}",text,offset,offset+512);
    } else strcpy(client->response,"{}");
    client->length=strlen(client->response);
    if(response_hook) {
        void (*hook)(void)=response_hook;response_hook=NULL;hook();
    }
    return (int64_t)client->length+declared_extra;
}
int esp_http_client_read(esp_http_client_handle_t client,char *data,int capacity)
{
    size_t size=client->length-client->read;
    if(size>(size_t)capacity)size=(size_t)capacity;
    memcpy(data,client->response+client->read,size);client->read+=size;
    return (int)size;
}
bool esp_http_client_is_complete_data_received(esp_http_client_handle_t client)
{ return client->read==client->length; }
int esp_http_client_get_status_code(esp_http_client_handle_t client) { return client->status; }
esp_err_t esp_http_client_set_timeout_ms(esp_http_client_handle_t client,int timeout)
{ if (strstr(client->url,"/events?")) stream_timeout_ms=timeout;return ESP_OK; }
esp_err_t esp_http_client_close(esp_http_client_handle_t client) { (void)client;return ESP_OK; }
esp_err_t esp_http_client_cleanup(esp_http_client_handle_t client)
{
    if(capture_mode && strstr(client->url,"/audio?"))upload_closed=true;
    free(client);return ESP_OK;
}

static void dispatch(void)
{
    command_t command;
    assert(xQueueReceive(s_commands,&command,0)==pdTRUE);
    execute(&command);
    s_view.busy=uxQueueMessagesWaiting(s_commands)>0;
}
static void setup(void)
{
    clear_message_cache();
    memset(&s_view,0,sizeof(s_view));
    strcpy(s_view.session_id,session_id);strcpy(s_view.text_id,session_id);
    s_view.selection=1;s_view.connected=true;s_view.text_kind=AGENT_TEXT_DRAFT;
    s_view.text_request=1;s_view.chat_request=1;
    s_view.draft_revision=s_view.text.revision=8;
    queue_fixture.count=0;response_hook=NULL;capture_mode=false;
    injected_response=NULL;response_status=200;declared_extra=0;stream_timeout_ms=0;
    whole_input="user question";whole_reply="assistant answer";whole_draft="reviewed draft";
    s_resync=false;
}

static const char turn_response[]=
    "{\"message\":{\"id\":\"00000000-0000-4000-8000-000000000002\",\"state\":\"completed\","
    "\"revision\":8,\"input_bytes\":14000,\"reply_bytes\":15000},"
    "\"before\":\"00000000-0000-4000-8000-000000000004\",\"after\":\"\"}";

static void test_whole_pair_and_history(void)
{
    setup();
    char *input=malloc(14001),*reply=malloc(15001);assert(input&&reply);
    memset(input,'A',14000);input[14000]=0;memset(reply,'B',15000);reply[15000]=0;
    whole_input=input;whole_reply=reply;injected_response=turn_response;
    s_view.text_kind=AGENT_TEXT_REPLY;s_view.chat_loading=true;
    agent_view_t view=s_view;
    CHECK(poll_turn(&view));
    CHECK(strlen(s_input_text)==14000&&strlen(s_reply_text)==15000);
    CHECK(s_view.turn.present&&!s_view.chat_loading);
    CHECK(strcmp(s_view.text_id,message_id)==0);
    char *snapshot=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(snapshot&&strlen(snapshot)==15000);free(snapshot);
    agent_client_message_select(true);
    snapshot=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(snapshot&&strlen(snapshot)==14000);free(snapshot);
    const uint32_t generation=s_view.chat_request;
    CHECK(agent_client_history(-1)==ESP_OK);
    CHECK(s_view.chat_request>generation&&s_view.chat_loading);
    CHECK(strcmp(s_view.turn_anchor,selected_message_id)==0);
    CHECK(agent_client_text_copy(s_view.selection+1,s_view.text_generation)==NULL);
    free(input);free(reply);
}

static void test_sse_append_and_recovery_offsets(void)
{
    setup();s_view.text_kind=AGENT_TEXT_REPLY;
    strcpy(s_cached_message,message_id);strcpy(s_view.message.id,message_id);
    s_view.turn.present=true;
    CHECK(append_reply(message_id,0,"你好"));
    CHECK(append_reply(message_id,0,"你好"));
    CHECK(strcmp(s_reply_text,"你好")==0);
    CHECK(append_reply(message_id,3,"好，世界"));
    CHECK(strcmp(s_reply_text,"你好，世界")==0);
    CHECK(!append_reply(message_id,99,"gap"));
    CHECK(!append_reply(message_id,0,"different"));
    CHECK(strcmp(s_reply_text,"你好，世界")==0);
    CHECK(append_reply(selected_message_id,0,"other message"));
    watch_context_t context={.selection=s_view.selection,.request=s_view.chat_request};
    strcpy(context.session,session_id);
    const char *event="event: chat.delta\ndata: {\"type\":\"chat.delta\",\"message_id\":\"00000000-0000-4000-8000-000000000002\",\"offset\":15,\"text\":\"！\"}\n\n";
    agent_sse_t stream={0};
    for(size_t i=0;i<strlen(event);i++)CHECK(agent_sse_feed(&stream,event+i,1,stream_event,&context));
    CHECK(strcmp(s_reply_text,"你好，世界！")==0);
    agent_client_message_select(true);
    CHECK(watch_current(&context));
    s_view.chat_request++;
    CHECK(!watch_current(&context));
}

static bool ends_with_truncated_notice(const char *text)
{
    const size_t length=strlen(text),notice_length=strlen(AGENT_TRUNCATED_NOTICE);
    return length>=notice_length&&strcmp(text+length-notice_length,AGENT_TRUNCATED_NOTICE)==0;
}
static void test_oversized_content_is_bounded(void)
{
    setup();
    const size_t limit=AGENT_CONTENT_MAX_BYTES;
    // The cut lands inside a three-byte character, which must be dropped whole.
    const size_t reply_total=limit-1+3+100,input_total=limit+4000;
    char *input=malloc(input_total+1),*reply=malloc(reply_total+1);assert(input&&reply);
    memset(input,'A',input_total);input[input_total]=0;
    memset(reply,'B',limit-1);memcpy(reply+limit-1,"你",3);memset(reply+limit+2,'C',100);reply[reply_total]=0;
    char response[256];
    snprintf(response,sizeof(response),
        "{\"message\":{\"id\":\"%s\",\"state\":\"completed\",\"revision\":8,\"input_bytes\":%zu,\"reply_bytes\":%zu},"
        "\"before\":\"\",\"after\":\"\"}",message_id,input_total,reply_total);
    whole_input=input;whole_reply=reply;injected_response=response;
    s_view.text_kind=AGENT_TEXT_REPLY;s_view.chat_loading=true;
    agent_view_t view=s_view;
    CHECK(poll_turn(&view));
    CHECK(strlen(s_reply_text)==limit-1&&s_reply_truncated&&s_reply_total==reply_total);
    CHECK(strlen(s_input_text)==limit&&s_input_truncated&&s_input_total==input_total);
    CHECK(s_view.message.reply_bytes==reply_total);
    char *snapshot=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(snapshot&&strncmp(snapshot,reply,limit-1)==0&&ends_with_truncated_notice(snapshot));free(snapshot);
    agent_client_message_select(true);
    snapshot=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(snapshot&&ends_with_truncated_notice(snapshot));free(snapshot);
    // An unchanged truncated reply is not downloaded again on the next poll.
    agent_client_message_select(false);
    whole_reply="refetched";view=s_view;
    CHECK(poll_turn(&view));
    CHECK(strlen(s_reply_text)==limit-1&&s_reply_truncated);
    free(input);free(reply);

    // Streaming stops growing the cache at the bound but keeps tracking offsets.
    setup();s_view.text_kind=AGENT_TEXT_REPLY;
    strcpy(s_cached_message,message_id);strcpy(s_view.message.id,message_id);
    s_view.turn.present=true;
    char *start=malloc(limit-1);assert(start);
    memset(start,'C',limit-2);start[limit-2]=0;
    CHECK(append_reply(message_id,0,start));free(start);
    CHECK(!s_reply_truncated&&s_reply_capacity<=limit+1);
    CHECK(append_reply(message_id,limit-2,"你好"));
    CHECK(strlen(s_reply_text)==limit-2&&s_reply_truncated&&s_reply_total==limit+4);
    CHECK(s_reply_capacity<=limit+1&&s_view.message.reply_bytes==limit+4);
    CHECK(append_reply(message_id,limit+4,"z"));
    CHECK(s_reply_total==limit+5&&strlen(s_reply_text)==limit-2);
    CHECK(append_reply(message_id,limit,"好z"));
    CHECK(s_reply_total==limit+5);
    CHECK(!append_reply(message_id,limit+10,"gap"));
    CHECK(utf8_complete_prefix("你",1)==0&&utf8_complete_prefix("你",3)==3&&utf8_complete_prefix("a你",2)==1);
}

static bool count_event(const char *json,void *context)
{
    (void)json;(*(unsigned *)context)++;return true;
}
static void test_sse_fragment_bounds(void)
{
    agent_sse_t stream={0};unsigned count=0;
    CHECK(agent_sse_feed(&stream,": heartbeat\r\ndata: {}\r\n\r\n",strlen(": heartbeat\r\ndata: {}\r\n\r\n"),count_event,&count));
    CHECK(count==1);
    memset(&stream,0,sizeof(stream));
    char oversized[AGENT_SSE_LINE_BYTES+1];memset(oversized,'x',sizeof(oversized));
    CHECK(!agent_sse_feed(&stream,oversized,sizeof(oversized),count_event,&count));
}

static void send_during_draft_response(void)
{
    const char *previous=injected_response;
    injected_response="{\"id\":\"00000000-0000-4000-8000-000000000002\",\"state\":\"queued\",\"revision\":1,\"input_bytes\":14,\"reply_bytes\":0}";
    response_status=202;
    CHECK(agent_client_send(s_view.draft_revision,s_view.selection)==ESP_OK);dispatch();
    injected_response=previous;
}
static void test_send_rejects_inflight_draft_response(void)
{
    setup();s_view.segment_count=s_view.segment_total=1;
    s_input_text=malloc(15);assert(s_input_text);strcpy(s_input_text,"reviewed draft");s_input_loaded=true;
    s_view.text.total=14;
    agent_view_t before=s_view;
    injected_response="{\"revision\":8,\"total\":1,\"items\":[{\"id\":\"00000000-0000-4000-8000-000000000003\",\"state\":\"ready\",\"bytes\":14,\"text\":\"reviewed draft\"}]}";
    response_hook=send_during_draft_response;
    CHECK(poll_draft(&before));
    CHECK(s_view.text_kind==AGENT_TEXT_REPLY&&s_view.segment_total==0);
    CHECK(strcmp(s_input_text,"reviewed draft")==0&&s_view.turn.present);
    CHECK(s_view.text_request>before.text_request);
}

static void test_cached_draft_review_and_reopen(void)
{
    setup();s_view.segment_count=s_view.segment_total=1;s_view.text_loading=s_view.chat_loading=true;
    agent_view_t view=s_view;CHECK(poll_full_draft(&view));
    CHECK(!s_view.text_loading&&!s_view.chat_loading&&s_view.text.total==14&&s_view.text.revision==8);
    CHECK(agent_client_review_draft()==ESP_OK);dispatch();
    view=s_view;CHECK(poll_full_draft(&view));
    CHECK(!s_view.text_loading&&!s_view.chat_loading&&s_view.text.total==14&&s_view.text.revision==8);
    command_t close={.kind=CMD_CLOSE};execute(&close);
    CHECK(s_input_text==NULL&&s_reply_text==NULL);
    agent_session_t session={0};strcpy(session.id,session_id);strcpy(session.title,"saved draft");
    CHECK(agent_client_open(&session)==ESP_OK);dispatch();
    CHECK(s_view.segments_loading);
    injected_response="{\"revision\":8,\"total\":1,\"items\":[{\"id\":\"00000000-0000-4000-8000-000000000003\",\"state\":\"ready\",\"bytes\":14,\"text\":\"reviewed draft\"}]}";
    view=s_view;CHECK(poll_draft(&view));
    CHECK(s_view.text_kind==AGENT_TEXT_DRAFT);
    view=s_view;CHECK(poll_full_draft(&view));
    CHECK(strcmp(s_input_text,"reviewed draft")==0);
}

static void test_empty_draft_returns_to_whole_turn(void)
{
    setup();s_view.text_loading=true;
    injected_response="{\"revision\":9,\"total\":0,\"items\":[]}";
    agent_view_t view=s_view;
    CHECK(poll_draft(&view));
    CHECK(s_view.text_kind==AGENT_TEXT_REPLY&&!s_view.text_loading&&s_view.chat_loading&&s_resync);
}

static void test_reply_stream_uses_http_reader(void)
{
    setup();s_view.text_kind=AGENT_TEXT_REPLY;
    strcpy(s_cached_message,message_id);strcpy(s_view.message.id,message_id);
    strcpy(s_view.message.state,"waiting");s_view.turn.present=true;
    injected_response="event: chat.delta\ndata: {\"type\":\"chat.delta\",\"message_id\":\"00000000-0000-4000-8000-000000000002\",\"text\":\"streamed reply\"}\n\n";
    agent_view_t view=s_view;
    CHECK(!watch_reply(&view));
    CHECK(stream_timeout_ms>0&&stream_timeout_ms<=40);
    CHECK(s_reply_text&&strcmp(s_reply_text,"streamed reply")==0);
    CHECK(strcmp(s_view.message.state,"receiving")==0);
}

static void test_raw_content_integrity(void)
{
    setup();char *text=NULL;content_headers_t headers;
    CHECK(read_content("/draft/content",&text,&headers)==200);
    CHECK(text&&strcmp(text,"reviewed draft")==0&&headers.revision==8);free(text);
    whole_draft="\xC3";
    CHECK(read_content("/draft/content",&text,&headers)==0&&text==NULL);
    whole_draft="short";declared_extra=3;
    CHECK(read_content("/draft/content",&text,&headers)==0&&text==NULL);
}

static void test_retry_accepts_server_message(void)
{
    setup();s_view.text_kind=AGENT_TEXT_REPLY;
    injected_response=retry_response;
    CHECK(agent_client_retry(message_id,s_view.selection)==ESP_OK);dispatch();
    CHECK(strcmp(s_view.message.id,message_id)==0&&strcmp(s_view.message.state,"queued")==0);
    CHECK(s_view.chat_loading);
}
static void test_capture(enum capture_case test_case)
{
    setup();capture_mode=true;capture_case=test_case;
    s_view.capture=AGENT_PREPARING;s_held=true;s_cancelled=false;
    capture_running=upload_closed=stage_ready=stage_exists=server_committed=false;
    cancel_accepted=audio_eof=false;
    chunks_read=cancel_attempts=commit_requests=commit_applies=abort_requests=0;
    server_draft='A';
    bool success=upload_audio(session_id,"00000000-0000-4000-8000-000000000002",1);
    switch(test_case) {
    case CAPTURE_NORMAL:
        CHECK(success && audio_eof && !s_cancelled);
        CHECK(commit_requests==1 && commit_applies==1 && server_draft=='B');
        break;
    case CAPTURE_CANCEL_RECORDING:
    case CAPTURE_CANCEL_FINALIZING:
    case CAPTURE_CANCEL_BEFORE_COMMIT: {
        CHECK(!success && cancel_accepted && s_cancelled);
        CHECK(commit_requests==0 && server_draft=='A');
        bool committed=true;
        CHECK(resolve_recording(session_id,"00000000-0000-4000-8000-000000000002",&committed));
        CHECK(!committed && abort_requests==1 && !stage_exists && server_draft=='A');
        break;
    }
    case CAPTURE_EXIT_COMMITTING:
        CHECK(success && cancel_attempts==1 && !cancel_accepted && !s_cancelled);
        CHECK(commit_requests==1 && commit_applies==1 && abort_requests==0 && server_draft=='B');
        break;
    case CAPTURE_LOST_ACK_ONCE:
        CHECK(success && commit_requests==2 && commit_applies==1 && server_draft=='B');
        break;
    case CAPTURE_LOST_ACK_TWICE: {
        CHECK(!success && commit_requests==2 && commit_applies==1 && server_draft=='B');
        bool committed=false;
        CHECK(resolve_recording(session_id,"00000000-0000-4000-8000-000000000002",&committed));
        CHECK(committed && server_draft=='B' && abort_requests==1);
        break;
    }
    case CAPTURE_SHORT:
        CHECK(!success && commit_requests==0 && server_draft=='A');
        break;
    }
    printf("capture case=%d success=%d cancel_accepted=%d commit_requests=%u applies=%u draft=%c\n",
        test_case,success,cancel_accepted,commit_requests,commit_applies,server_draft);
    capture_mode=false;
}

int main(void)
{
    assert(agent_client_init()==ESP_OK);
    test_whole_pair_and_history();
    test_sse_append_and_recovery_offsets();
    test_oversized_content_is_bounded();
    test_sse_fragment_bounds();
    test_send_rejects_inflight_draft_response();
    test_cached_draft_review_and_reopen();
    test_raw_content_integrity();
    test_empty_draft_returns_to_whole_turn();
    test_reply_stream_uses_http_reader();
    test_retry_accepts_server_message();
    for(enum capture_case item=CAPTURE_NORMAL;item<=CAPTURE_SHORT;item++)test_capture(item);
    clear_message_cache();
    printf("client regression assertions failed=%u\n",failed);
    return failed?1:0;
}
