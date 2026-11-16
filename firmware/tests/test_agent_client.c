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
    char request_body[16384];
    size_t written;
};
static int response_status = 200;
static int stream_timeout_ms;
static const char *injected_response;
static const char *whole_input, *whole_reply, *whole_other;
static int declared_extra;
static const char *message_id = "00000000-0000-4000-8000-000000000002";
static const char *selected_message_id = "00000000-0000-4000-8000-000000000004";
static const char retry_response[] =
    "{\"id\":\"00000000-0000-4000-8000-000000000002\",\"state\":\"queued\","
    "\"revision\":4,\"input_bytes\":1,\"reply_bytes\":9}";
static const char send_response[] =
    "{\"id\":\"00000000-0000-4000-8000-000000000002\",\"state\":\"queued\",\"revision\":1,"
    "\"input_bytes\":12,\"reply_bytes\":0,\"session\":{\"id\":\"x\",\"title\":\"测试转写\",\"state\":\"ready\"}}";
static void (*response_hook)(void);

// Every request, in order, as "<method letter> <path>\n" for the assertions.
static char request_log[8192];
static unsigned requests;
static char last_body[16384];
static unsigned dropped_sends; // Message POSTs whose response is lost.

// Capture scheduling hooks are deterministic points in actual upload_audio().
enum capture_case {
    CAPTURE_NORMAL, CAPTURE_CANCEL_RECORDING, CAPTURE_CANCEL_FINALIZING, CAPTURE_SHORT,
    CAPTURE_NO_SPEECH, CAPTURE_TOO_LARGE, CAPTURE_TOO_MANY_CHARS,
};
static bool capture_mode, capture_running, cancel_accepted, audio_eof;
static char trace_body[16384];
static size_t trace_written;
static unsigned trace_uploads;
static int trace_status = 201;
static unsigned chunks_read, cancel_attempts;
static enum capture_case capture_case;
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
int xSemaphoreTake(SemaphoreHandle_t handle, unsigned timeout) { (void)handle;(void)timeout;return pdTRUE; }
int xSemaphoreGive(SemaphoreHandle_t handle) { (void)handle;return pdTRUE; }
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
void esp_fill_random(void *data, size_t size) { static unsigned char seed;memset(data, ++seed, size); }
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
void agent_capture_release(void) {}
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
    const char *path=strstr(client->url,"/api/v1");
    char line[400];
    snprintf(line,sizeof(line),"%s %s\n",method_name(client->method),path?path+7:client->url);
    if(strlen(request_log)+strlen(line)<sizeof(request_log))strcat(request_log,line);
    requests++;
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
    if(strstr(client->url,"/device/logs")) {
        assert(trace_written+(size_t)size<sizeof(trace_body));
        memcpy(trace_body+trace_written,data,(size_t)size);
        trace_written+=(size_t)size;
        return size;
    }
    if(strstr(client->url,"/audio?")) {
        if(size==5 && memcmp(data,"0\r\n\r\n",5)==0)audio_eof=true;
        return size;
    }
    assert(client->written+(size_t)size<sizeof(client->request_body));
    memcpy(client->request_body+client->written,data,(size_t)size);
    client->written+=(size_t)size;
    client->request_body[client->written]=0;
    return size;
}
static int64_t respond(esp_http_client_handle_t client,int status,const char *body)
{
    client->status=status;
    snprintf(client->response,sizeof(client->response),"%s",body);
    client->length=strlen(client->response);
    return (int64_t)client->length;
}
int64_t esp_http_client_fetch_headers(esp_http_client_handle_t client)
{
    snprintf(last_body,sizeof(last_body),"%s",client->request_body);
    if(strstr(client->url,"/device/logs")) {
        trace_uploads++;
        return respond(client,trace_status,"{\"id\":\"x\"}");
    }
    if(strstr(client->url,"/audio?")) {
        CHECK(audio_eof);
        CHECK(s_view.capture==AGENT_FINALIZING);
        if(capture_case==CAPTURE_CANCEL_FINALIZING)try_cancel();
        if(capture_case==CAPTURE_NO_SPEECH)return respond(client,422,"{\"error\":{\"code\":\"no_speech\"}}");
        if(capture_case==CAPTURE_TOO_LARGE) {
            client->status=200;
            memset(client->response,'a',AGENT_DRAFT_MAX_BYTES+1);
            client->length=AGENT_DRAFT_MAX_BYTES+1;
            return (int64_t)client->length;
        }
        if(capture_case==CAPTURE_TOO_MANY_CHARS) {
            client->status=200;
            memset(client->response,'a',AGENT_DRAFT_MAX_CHARS+1);
            client->response[AGENT_DRAFT_MAX_CHARS+1]=0;
            client->length=AGENT_DRAFT_MAX_CHARS+1;
            return (int64_t)client->length;
        }
        return respond(client,200,"测试转写");
    }
    if(client->method==HTTP_METHOD_POST && strstr(client->url,"/api/v1/transcriptions") &&
       !strstr(client->url,"/audio"))
        return respond(client,201,"{\"id\":\"x\"}");
    if(client->method==HTTP_METHOD_POST && strstr(client->url,"/messages") && !strstr(client->url,"/retry") &&
       dropped_sends>0) {
        dropped_sends--;
        return -1;
    }
    client->status=response_status;
    if (strstr(client->url,"/content") != NULL) {
        const char *value=strstr(client->url,"part=input")?whole_input:
            strstr(client->url,"part=reply")?whole_reply:whole_other;
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
esp_err_t esp_http_client_cleanup(esp_http_client_handle_t client) { free(client);return ESP_OK; }

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
    discard_draft();
    memset(&s_view,0,sizeof(s_view));
    strcpy(s_view.session_id,session_id);strcpy(s_view.text_id,session_id);
    s_view.selection=1;s_view.connected=true;s_view.text_kind=AGENT_TEXT_REPLY;
    s_view.text_request=1;s_view.chat_request=1;
    queue_fixture.count=0;response_hook=NULL;capture_mode=false;capture_case=CAPTURE_NORMAL;
    injected_response=NULL;response_status=200;declared_extra=0;stream_timeout_ms=0;dropped_sends=0;
    whole_input="user question";whole_reply="assistant answer";whole_other="other content";
    request_log[0]=0;requests=0;last_body[0]=0;
    s_pending_message[0]=0;
    s_resync=false;
}
// A draft the device already holds in RAM, shown on the voice page.
static void hold_draft(const char *text)
{
    discard_draft();
    s_draft=malloc(strlen(text)+1);assert(s_draft);strcpy(s_draft,text);
    s_view.draft_chars=utf8_chars(text);
    s_view.draft_serial++;
    s_view.text_kind=AGENT_TEXT_DRAFT;
    strcpy(s_view.text_id,s_view.session_id);
}
// Arms the capture fake for one recording through record().
static void arm_capture(enum capture_case test_case)
{
    capture_mode=true;capture_case=test_case;
    capture_running=cancel_accepted=audio_eof=false;
    chunks_read=cancel_attempts=0;
    s_cancelled=false;
}

static const char turn_response[]=
    "{\"message\":{\"id\":\"00000000-0000-4000-8000-000000000002\",\"state\":\"completed\","
    "\"revision\":8,\"input_bytes\":6000,\"reply_bytes\":7000},"
    "\"before\":\"00000000-0000-4000-8000-000000000004\",\"after\":\"\"}";

static void test_whole_pair_and_history(void)
{
    setup();
    char *input=malloc(6001),*reply=malloc(7001);assert(input&&reply);
    memset(input,'A',6000);input[6000]=0;memset(reply,'B',7000);reply[7000]=0;
    whole_input=input;whole_reply=reply;injected_response=turn_response;
    s_view.text_kind=AGENT_TEXT_REPLY;s_view.chat_loading=true;
    agent_view_t view=s_view;
    CHECK(poll_turn(&view));
    CHECK(strlen(s_input_text)==6000&&strlen(s_reply_text)==7000);
    CHECK(s_view.turn.present&&!s_view.chat_loading);
    CHECK(strcmp(s_view.text_id,message_id)==0);
    char *snapshot=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(snapshot&&strlen(snapshot)==7000);free(snapshot);
    agent_client_message_select(true);
    snapshot=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(snapshot&&strlen(snapshot)==6000);free(snapshot);
    const uint32_t generation=s_view.chat_request;
    CHECK(agent_client_history(-1)==ESP_OK);
    CHECK(s_view.chat_request>generation&&s_view.chat_loading);
    CHECK(strcmp(s_view.turn_anchor,selected_message_id)==0);
    CHECK(agent_client_text_copy(s_view.selection+1,s_view.text_generation)==NULL);
    free(input);free(reply);
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

    CHECK(utf8_complete_prefix("你",1)==0&&utf8_complete_prefix("你",3)==3&&utf8_complete_prefix("a你",2)==1);
}

static void test_raw_content_integrity(void)
{
    setup();char *text=NULL;content_headers_t headers;
    const char *path="/sessions/x/messages/y/content";
    CHECK(read_content(path,&text,&headers)==200);
    CHECK(text&&strcmp(text,"other content")==0&&headers.revision==8);free(text);
    whole_other="\xC3";
    CHECK(read_content(path,&text,&headers)==0&&text==NULL);
    whole_other="short";declared_extra=3;
    CHECK(read_content(path,&text,&headers)==0&&text==NULL);
}

static void test_active_reply_is_fetched_once_complete(void)
{
    setup();s_view.text_kind=AGENT_TEXT_REPLY;s_view.chat_loading=true;
    char response[256];
    snprintf(response,sizeof(response),"{\"message\":{\"id\":\"%s\",\"state\":\"receiving\",\"revision\":3,"
        "\"input_bytes\":2,\"reply_bytes\":5},\"before\":\"\",\"after\":\"\"}",message_id);
    whole_input="hi";whole_reply="hello";injected_response=response;
    agent_view_t view=s_view;
    // While Hermes writes, only the state is tracked; no partial text is downloaded.
    CHECK(poll_turn(&view));
    CHECK(!s_reply_loaded&&strcmp(s_view.message.state,"receiving")==0);
    view=s_view;
    CHECK(poll_reply_state(&view));
    CHECK(!s_reply_loaded);
    snprintf(response,sizeof(response),"{\"message\":{\"id\":\"%s\",\"state\":\"completed\",\"revision\":4,"
        "\"input_bytes\":2,\"reply_bytes\":5},\"before\":\"\",\"after\":\"\"}",message_id);
    view=s_view;
    CHECK(poll_reply_state(&view));
    CHECK(s_reply_loaded&&strcmp(s_reply_text,"hello")==0&&strcmp(s_view.message.state,"completed")==0);
}

static void test_retry_accepts_server_message(void)
{
    setup();s_view.text_kind=AGENT_TEXT_REPLY;
    injected_response=retry_response;
    CHECK(agent_client_retry(message_id,s_view.selection)==ESP_OK);dispatch();
    CHECK(strcmp(s_view.message.id,message_id)==0&&strcmp(s_view.message.state,"queued")==0);
    CHECK(s_view.chat_loading);
}

static void test_upload(enum capture_case test_case)
{
    setup();arm_capture(test_case);
    s_view.capture=AGENT_PREPARING;s_held=true;
    int status=-1;
    char *text=upload_audio("00000000-0000-4000-8000-000000000009",&status);
    CHECK(strstr(request_log,"P /transcriptions/00000000-0000-4000-8000-000000000009/audio?sample_rate=16000")!=NULL);
    switch(test_case) {
    case CAPTURE_NORMAL:
        CHECK(text&&strcmp(text,"测试转写")==0&&status==200&&audio_eof&&!s_cancelled);
        break;
    case CAPTURE_CANCEL_RECORDING:
    case CAPTURE_CANCEL_FINALIZING:
        CHECK(!text&&cancel_accepted&&s_cancelled);
        break;
    case CAPTURE_SHORT:
        CHECK(!text&&status==0&&!audio_eof);
        break;
    case CAPTURE_NO_SPEECH:
        CHECK(!text&&status==422);
        break;
    case CAPTURE_TOO_LARGE:
        CHECK(!text&&status==413);
        break;
    case CAPTURE_TOO_MANY_CHARS:
        CHECK(text&&utf8_chars(text)==AGENT_DRAFT_MAX_CHARS+1); // record() rejects it.
        break;
    }
    printf("upload case=%d status=%d text=%d cancel_accepted=%d\n",test_case,status,text!=NULL,cancel_accepted);
    free(text);
    capture_mode=false;
}

// From the session list: the new chat stays local while recording and
// reviewing; the first send creates and names it on the gateway.
static void test_new_chat_is_created_by_its_first_send(void)
{
    setup();s_view.session_id[0]=0;s_view.text_id[0]=0;
    arm_capture(CAPTURE_NORMAL);
    CHECK(agent_client_record(true)==ESP_OK);dispatch();
    CHECK(s_view.session_local&&strcmp(s_view.title,"新会话")==0&&agent_valid_id(s_view.session_id));
    CHECK(s_view.capture==AGENT_IDLE&&s_view.text_kind==AGENT_TEXT_DRAFT&&s_view.draft_chars==4);
    CHECK(s_view.capture_serial==1&&strstr(request_log,"/sessions")==NULL);
    char *shown=agent_client_text_copy(s_view.selection,s_view.text_generation);
    CHECK(shown&&strcmp(shown,"测试转写")==0);free(shown);
    char session[37];strcpy(session,s_view.session_id);
    capture_mode=false;
    request_log[0]=0;
    injected_response=send_response;response_status=202;
    CHECK(agent_client_send(s_view.draft_serial,s_view.selection)==ESP_OK);dispatch();
    char expected[96];snprintf(expected,sizeof(expected),"P /sessions/%s/messages\n",session);
    CHECK(strcmp(request_log,expected)==0);
    cJSON *body=cJSON_Parse(last_body);
    CHECK(body&&strcmp(cJSON_GetObjectItem(body,"text")->valuestring,"测试转写")==0&&
          cJSON_IsTrue(cJSON_GetObjectItem(body,"new_session"))&&agent_valid_id(cJSON_GetObjectItem(body,"id")->valuestring));
    cJSON_Delete(body);
    CHECK(!s_view.session_local&&strcmp(s_view.title,"测试转写")==0);
    CHECK(s_view.draft_chars==0&&s_draft==NULL&&s_view.text_kind==AGENT_TEXT_REPLY);
    CHECK(strcmp(s_input_text,"测试转写")==0&&s_view.turn.present&&s_view.chat_loading);
}

// A recording that fails, hears nothing or is too long leaves the shown draft.
static void test_failed_rerecord_keeps_draft(enum capture_case test_case, const char *notice_text)
{
    setup();hold_draft("旧草稿");
    const uint32_t serial=s_view.draft_serial;
    arm_capture(test_case);
    CHECK(agent_client_record(false)==ESP_OK);dispatch();
    CHECK(s_draft&&strcmp(s_draft,"旧草稿")==0&&s_view.draft_serial==serial&&s_view.draft_chars==3);
    CHECK(s_view.text_kind==AGENT_TEXT_DRAFT&&s_view.capture==AGENT_IDLE&&s_view.capture_serial==0);
    CHECK(strcmp(s_view.notice,notice_text)==0);
    capture_mode=false;
}

static void test_rerecord_replaces_draft(void)
{
    setup();hold_draft("旧草稿");
    const uint32_t serial=s_view.draft_serial;
    arm_capture(CAPTURE_NORMAL);
    CHECK(agent_client_record(false)==ESP_OK);dispatch();
    CHECK(strcmp(s_draft,"测试转写")==0&&s_view.draft_serial!=serial&&s_view.draft_chars==4);
    CHECK(strstr(request_log,"P /transcriptions\n")!=NULL&&strstr(request_log,"/sessions")==NULL);
    capture_mode=false;
}

// The sends of one draft share a message ID, so a lost response is retried
// without storing the message twice; a new draft gets a new ID.
static void test_lost_send_response_reuses_ids(void)
{
    setup();hold_draft("再说一遍");
    injected_response=send_response;response_status=202;dropped_sends=1;
    CHECK(agent_client_send(s_view.draft_serial,s_view.selection)==ESP_OK);dispatch();
    char expected[160];
    snprintf(expected,sizeof(expected),"P /sessions/%s/messages\nP /sessions/%s/messages\n",session_id,session_id);
    CHECK(strcmp(request_log,expected)==0&&s_draft==NULL);
    cJSON *body=cJSON_Parse(last_body);
    CHECK(body&&!cJSON_IsTrue(cJSON_GetObjectItem(body,"new_session")));
    char first[37];strcpy(first,cJSON_GetObjectItem(body,"id")->valuestring);
    cJSON_Delete(body);
    // Both attempts failing keeps the draft and its ID for the next press.
    hold_draft("第二条");dropped_sends=2;request_log[0]=0;
    CHECK(agent_client_send(s_view.draft_serial,s_view.selection)==ESP_OK);dispatch();
    CHECK(s_draft&&strcmp(s_draft,"第二条")==0&&strcmp(s_view.notice,"连接失败，请重试")==0);
    char retained[37];strcpy(retained,s_pending_message);
    CHECK(strcmp(retained,first)!=0);
    CHECK(agent_client_send(s_view.draft_serial,s_view.selection)==ESP_OK);dispatch();
    body=cJSON_Parse(last_body);
    CHECK(body&&strcmp(cJSON_GetObjectItem(body,"id")->valuestring,retained)==0);
    cJSON_Delete(body);
}

// Clearing is local and invalidates a send still waiting in the queue.
static void test_clear_is_local_and_cancels_queued_send(void)
{
    setup();hold_draft("不要了");
    CHECK(agent_client_send(s_view.draft_serial,s_view.selection)==ESP_OK);
    CHECK(agent_client_clear_draft()==ESP_OK);
    CHECK(s_draft==NULL&&s_view.draft_chars==0&&s_view.text_kind==AGENT_TEXT_REPLY);
    dispatch();
    CHECK(requests==0&&strcmp(s_view.notice,"内容已更新，请稍候")==0);
    CHECK(agent_client_clear_draft()==ESP_ERR_INVALID_STATE);
    CHECK(agent_client_send(s_view.draft_serial,s_view.selection)==ESP_ERR_INVALID_STATE);
}

static void test_leaving_session_discards_draft(void)
{
    setup();hold_draft("离开就丢");
    CHECK(agent_client_close_session()==ESP_OK);dispatch();
    CHECK(s_draft==NULL&&s_view.draft_chars==0&&s_view.session_id[0]==0&&!s_view.session_local);
}

// The live transcript is polled by transcription ID while recording.
static void test_live_transcript_polls_side_channel(void)
{
    setup();
    s_view.capture=AGENT_RECORDING;s_view.text_kind=AGENT_TEXT_SEGMENT;
    strcpy(s_view.text_id,"00000000-0000-4000-8000-000000000009");
    agent_view_t view=s_view;
    CHECK(poll_text(&view));
    CHECK(strcmp(request_log,"G /transcriptions/00000000-0000-4000-8000-000000000009/text?limit=512&tail=1\n")==0);
    CHECK(s_view.text.total==1536&&!s_view.text_loading);
    response_status=404;view=s_view;
    CHECK(poll_text(&view)); // Not streaming yet: not a read failure.
}

// Undo HTTP/1.1 chunked framing written by the trace upload.
static size_t unchunk(const char *in, size_t length, char *out)
{
    size_t used = 0, i = 0;
    while (i < length) {
        char *end = NULL;
        const unsigned long size = strtoul(in + i, &end, 16);
        i = (size_t)(end - in) + 2;
        if (size == 0) break;
        memcpy(out + used, in + i, size);
        used += size;
        i += size + 2;
    }
    out[used] = 0;
    return used;
}

// A message sent from the web page becomes the newest turn; an open, settled
// newest turn follows it, while reading older history does not jump.
static void test_follows_newer_turn_from_other_client(void)
{
    static const char newer[]=
        "{\"message\":{\"id\":\"00000000-0000-4000-8000-000000000003\",\"state\":\"waiting\","
        "\"revision\":1,\"input_bytes\":5,\"reply_bytes\":0},"
        "\"before\":\"00000000-0000-4000-8000-000000000002\",\"after\":\"\"}";
    setup();
    injected_response=turn_response;
    s_view.text_kind=AGENT_TEXT_REPLY;s_view.chat_loading=true;
    agent_view_t view=s_view;
    CHECK(poll_turn(&view));
    CHECK(strcmp(s_view.turn_anchor,"00000000-0000-4000-8000-000000000002")==0);
    // Same newest message: nothing changes.
    view=s_view;
    const uint32_t generation=s_view.text_generation;
    CHECK(poll_latest(&view));
    CHECK(s_view.text_generation==generation);
    // A newer message appears.
    whole_input="hello";whole_reply="";injected_response=newer;
    s_view.text_kind=AGENT_TEXT_INPUT;
    view=s_view;
    CHECK(poll_latest(&view));
    CHECK(strcmp(s_view.turn_anchor,"00000000-0000-4000-8000-000000000003")==0);
    CHECK(s_view.text_kind==AGENT_TEXT_REPLY && strcmp(s_input_text,"hello")==0);
    // Reading an older exchange (one with a newer neighbour) stays put.
    strcpy(s_view.turn.after,"00000000-0000-4000-8000-000000000009");
    strcpy(s_view.turn_anchor,"00000000-0000-4000-8000-000000000002");
    view=s_view;
    CHECK(poll_latest(&view));
    CHECK(strcmp(s_view.turn_anchor,"00000000-0000-4000-8000-000000000002")==0);
}

static void test_failure_report_upload(void)
{
    setup();
    s_view.capture = AGENT_IDLE;
    trace_written = 0; trace_uploads = 0; trace_status = 503;
    char ok_body[64];
    request(HTTP_METHOD_POST, "/sessions/00000000-0000-4000-8000-000000000001/messages", "{}", ok_body, sizeof(ok_body));
    agent_trace_request(TRACE_TRIGGER_RECORD_FAILED);
    report_trace();
    CHECK(trace_uploads == 1 && agent_trace_pending() == TRACE_TRIGGER_RECORD_FAILED); // Failed upload stays pending.
    report_trace();
    CHECK(trace_uploads == 1); // Rate limited.
    trace_written = 0; trace_status = 201;
    CHECK(upload_trace(TRACE_TRIGGER_RECORD_FAILED));
    agent_trace_reported(TRACE_TRIGGER_RECORD_FAILED, true, true);
    CHECK(agent_trace_pending() == TRACE_TRIGGER_NONE);
    static char json[16384];
    unchunk(trace_body, trace_written, json);
    cJSON *root = cJSON_Parse(json);
    CHECK(root != NULL);
    if (root != NULL) {
        CHECK(strcmp(cJSON_GetObjectItem(root, "trigger")->valuestring, "record_failed") == 0);
        bool saw_post = false;
        const cJSON *event;
        cJSON_ArrayForEach(event, cJSON_GetObjectItem(root, "events")) {
            if (cJSON_GetArrayItem(event, 2)->valueint == TRACE_HTTP &&
                strcmp(cJSON_GetArrayItem(event, 5)->valuestring, "P messages") == 0) saw_post = true;
        }
        CHECK(saw_post);
        cJSON_Delete(root);
    }
}

int main(void)
{
    assert(agent_client_init()==ESP_OK);
    test_whole_pair_and_history();
    test_oversized_content_is_bounded();
    test_raw_content_integrity();
    test_active_reply_is_fetched_once_complete();
    test_retry_accepts_server_message();
    for(enum capture_case item=CAPTURE_NORMAL;item<=CAPTURE_TOO_MANY_CHARS;item++)test_upload(item);
    test_new_chat_is_created_by_its_first_send();
    test_rerecord_replaces_draft();
    test_failed_rerecord_keeps_draft(CAPTURE_NO_SPEECH,"没有听清，请再说一次");
    test_failed_rerecord_keeps_draft(CAPTURE_TOO_LARGE,AGENT_DRAFT_TOO_LONG);
    test_failed_rerecord_keeps_draft(CAPTURE_TOO_MANY_CHARS,AGENT_DRAFT_TOO_LONG);
    test_lost_send_response_reuses_ids();
    test_clear_is_local_and_cancels_queued_send();
    test_leaving_session_discards_draft();
    test_live_transcript_polls_side_channel();
    test_failure_report_upload();
    test_follows_newer_turn_from_other_client();
    clear_message_cache();
    discard_draft();
    printf("client regression assertions failed=%u\n",failed);
    return failed?1:0;
}
