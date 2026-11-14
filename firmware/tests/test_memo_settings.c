#include <assert.h>
#include <stdio.h>
#include "../main/memo_settings.c"

static const char *keys[]={"vol","to","sl","tz","br","th"};
static int32_t disk[6];
static bool present[6];
static int init_failure, open_failure, fail_write, fail_commit;
static int writes, commits, audio_calls, audio_volume;
static int index_of(const char *key)
{ for(int i=0;i<6;i++)if(!strcmp(keys[i],key))return i;assert(0);return 0; }
esp_err_t nvs_flash_init(void) { return init_failure; }
esp_err_t nvs_open(const char *ns,int mode,nvs_handle_t *out)
{ assert(!strcmp(ns,"memo") && mode==NVS_READWRITE);if(open_failure)return open_failure;*out=1;return ESP_OK; }
esp_err_t nvs_get_i32(nvs_handle_t handle,const char *key,int32_t *out)
{ assert(handle==1);int i=index_of(key);if(!present[i])return ESP_ERR_NVS_NOT_FOUND;*out=disk[i];return ESP_OK; }
esp_err_t nvs_set_i32(nvs_handle_t handle,const char *key,int32_t value)
{
    assert(handle==1);int i=index_of(key);writes++;
    // Model writes reaching Flash even when the operation later reports failure.
    disk[i]=value;present[i]=true;
    if(fail_write>0){fail_write--;return ESP_FAIL;}return ESP_OK;
}
esp_err_t nvs_commit(nvs_handle_t handle)
{ assert(handle==1);commits++;if(fail_commit>0){fail_commit--;return ESP_FAIL;}return ESP_OK; }
void bsp_audio_set_volume(uint8_t value) { audio_calls++;audio_volume=value; }
static void boot(void)
{ s_ready=false;s_nvs=0;assert(memo_settings_init()==ESP_OK); }
static void defaults(void)
{
    memset(disk,0,sizeof(disk));memset(present,0,sizeof(present));
    init_failure=open_failure=fail_write=fail_commit=0;
    writes=commits=audio_calls=0;boot();
}
int main(void)
{
    memo_settings_edit_t edit={0};
    init_failure=ESP_ERR_NVS_NO_FREE_PAGES;
    assert(memo_settings_init()==ESP_ERR_NVS_NO_FREE_PAGES && !s_ready);
    assert(memo_settings_get()->volume==80);
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_VOLUME)==ESP_OK);
    memo_settings_edit_adjust(&edit,1);assert(edit.value.volume==100 && writes==0 && audio_calls==0);
    assert(memo_settings_edit_save(&edit)==ESP_ERR_INVALID_STATE && edit.active);
    memo_settings_edit_cancel(&edit);assert(!edit.active && writes==0);
    init_failure=ESP_ERR_NVS_NEW_VERSION_FOUND;
    assert(memo_settings_init()==ESP_ERR_NVS_NEW_VERSION_FOUND && writes==0);
    init_failure=0;open_failure=ESP_FAIL;
    assert(memo_settings_init()==ESP_FAIL && !s_ready);
    defaults();
    assert(memo_settings_get()->brightness==100 && !present[4] && writes==0);
    assert(memo_settings_brightness_step(100,1)==100);
    assert(memo_settings_brightness_step(10,-1)==10);
    assert(memo_settings_brightness_step(100,-1)==90);
    assert(memo_settings_brightness_step(10,1)==20);
    assert(memo_settings_brightness_step(50,0)==50);
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_BRIGHTNESS)==ESP_OK);
    memo_settings_edit_adjust(&edit,-1);
    assert(edit.value.brightness==90 && writes==0 && audio_calls==0);
    memo_settings_edit_cancel(&edit);
    assert(memo_settings_get()->brightness==100 && writes==0);
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_BRIGHTNESS)==ESP_OK);
    memo_settings_edit_adjust(&edit,-1);
    fail_write=1;
    assert(memo_settings_edit_save(&edit)==ESP_FAIL && edit.active);
    assert(memo_settings_get()->brightness==100 && disk[4]==100);
    fail_commit=1;
    assert(memo_settings_edit_save(&edit)==ESP_FAIL && edit.active);
    assert(memo_settings_get()->brightness==100 && disk[4]==100);
    assert(memo_settings_edit_save(&edit)==ESP_OK && !edit.active);
    boot();assert(memo_settings_get()->brightness==90 && audio_calls==0);
    assert(memo_settings_set_brightness(100)==ESP_OK);
    writes=commits=0;
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_VOLUME)==ESP_OK);
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_SLEEP)==ESP_ERR_INVALID_STATE);
    memo_settings_edit_adjust(&edit,1);assert(edit.value.volume==100 && writes==0 && audio_calls==0);
    memo_settings_edit_cancel(&edit);assert(!edit.active && memo_settings_get()->volume==80 && writes==0);
    assert(memo_settings_edit_save(&edit)==ESP_ERR_INVALID_STATE);
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_VOLUME)==ESP_OK);
    memo_settings_edit_adjust(&edit,1);
    fail_write=1;
    assert(memo_settings_edit_save(&edit)==ESP_FAIL && edit.active);
    assert(memo_settings_get()->volume==80 && disk[0]==80 && audio_calls==0); // rollback
    fail_commit=1;
    assert(memo_settings_edit_save(&edit)==ESP_FAIL && edit.active);
    assert(disk[0]==80 && memo_settings_get()->volume==80 && audio_calls==0);
    assert(memo_settings_edit_save(&edit)==ESP_OK && !edit.active);
    assert(memo_settings_get()->volume==100 && audio_volume==100 && audio_calls==1);
    boot();assert(memo_settings_get()->volume==100);
    assert(memo_settings_volume_step(100,1)==100);
    assert(memo_settings_volume_step(0,-1)==0);
    assert(memo_settings_volume_step(80,1)==100);
    assert(memo_settings_volume_step(80,-1)==60);
    assert(memo_settings_volume_step(80,0)==80);
    // Saving one selected field preserves unrelated runtime/storage changes.
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_TIMEOUT)==ESP_OK);
    memo_settings_edit_adjust(&edit,-1);assert(edit.value.screen_timeout_s==0);
    assert(memo_settings_set_volume(80)==ESP_OK);
    assert(memo_settings_edit_save(&edit)==ESP_OK);
    assert(memo_settings_get()->volume==80 && memo_settings_get()->screen_timeout_s==0);
    assert(memo_settings_edit_begin(&edit,MEMO_EDIT_SLEEP)==ESP_OK);
    memo_settings_edit_adjust(&edit,1);assert(edit.value.sleep_min==15);
    fail_commit=2; // Including failed rollback: still never publish or claim success.
    assert(memo_settings_edit_save(&edit)==ESP_FAIL && edit.active);
    assert(memo_settings_get()->sleep_min==5);
    assert(memo_settings_edit_save(&edit)==ESP_OK && !edit.active);
    boot();assert(memo_settings_get()->sleep_min==15);
    const int before=writes;
    assert(memo_settings_set_volume(13)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_volume(101)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_brightness(0)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_brightness(15)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_brightness(101)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_screen_timeout(31)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_sleep(2)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_tz(841)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_set_tz(-721)==ESP_ERR_INVALID_ARG);
    assert(writes==before);
    assert(memo_settings_set_tz(-720)==ESP_OK);boot();assert(memo_settings_get()->tz_offset_min==-720);
    assert(memo_settings_set_tz(840)==ESP_OK);boot();assert(memo_settings_get()->tz_offset_min==840);
    // Validate signed invalid values before narrowing; never silently rewrite bad keys.
    const int32_t invalid[][6]={{-176,286,261,66016,-156,999},{256,-1,-1,-66016,256,-1},{13,1,60,841,15,3}};
    for(size_t n=0;n<sizeof(invalid)/sizeof(invalid[0]);n++) {
        memcpy(disk,invalid[n],sizeof(invalid[n]));memset(present,1,sizeof(present));
        int old=writes;boot();const memo_settings_t *st=memo_settings_get();
        assert(st->volume==80 && st->brightness==100 && st->screen_timeout_s==30 && st->sleep_min==5 && st->tz_offset_min==480);
        assert(writes==old);
    }
    assert(memo_settings_edit_begin(NULL,MEMO_EDIT_VOLUME)==ESP_ERR_INVALID_ARG);
    assert(memo_settings_edit_begin(&edit,(memo_settings_field_t)99)==ESP_ERR_INVALID_ARG);
    defaults();
    assert(memo_settings_get()->theme == MEMO_THEME_AUTO);
    assert(memo_settings_edit_begin(&edit, MEMO_EDIT_THEME) == ESP_OK);
    memo_settings_edit_adjust(&edit, 1);
    assert(edit.value.theme == MEMO_THEME_LIGHT && memo_settings_get()->theme == MEMO_THEME_AUTO && writes == 0);
    memo_settings_edit_cancel(&edit);
    assert(memo_settings_get()->theme == MEMO_THEME_AUTO && writes == 0);
    assert(memo_settings_edit_begin(&edit, MEMO_EDIT_THEME) == ESP_OK);
    memo_settings_edit_adjust(&edit, -1);
    assert(edit.value.theme == MEMO_THEME_DARK);
    fail_commit = 1;
    assert(memo_settings_edit_save(&edit) == ESP_FAIL && edit.active);
    assert(memo_settings_get()->theme == MEMO_THEME_AUTO && disk[5] == MEMO_THEME_AUTO);
    assert(memo_settings_edit_save(&edit) == ESP_OK);
    boot();
    assert(memo_settings_get()->theme == MEMO_THEME_DARK);
    assert(memo_settings_set_theme(3) == ESP_ERR_INVALID_ARG);
    assert(memo_settings_edit_begin(&edit, MEMO_EDIT_TIMEZONE) == ESP_OK);
    memo_settings_edit_adjust(&edit, 1);
    assert(edit.value.tz_offset_min == 495 && memo_settings_get()->tz_offset_min == 480);
    memo_settings_edit_cancel(&edit);
    assert(memo_settings_get()->tz_offset_min == 480);
    assert(memo_settings_edit_begin(&edit, MEMO_EDIT_TIMEZONE) == ESP_OK);
    memo_settings_edit_adjust(&edit, -1);
    assert(memo_settings_edit_save(&edit) == ESP_OK);
    boot(); assert(memo_settings_get()->tz_offset_min == 465);
    assert(memo_settings_set_tz(840) == ESP_OK);
    assert(memo_settings_edit_begin(&edit, MEMO_EDIT_TIMEZONE) == ESP_OK);
    memo_settings_edit_adjust(&edit, 1); assert(edit.value.tz_offset_min == 840);
    memo_settings_edit_cancel(&edit);
    assert(memo_settings_set_tz(-720) == ESP_OK);
    assert(memo_settings_edit_begin(&edit, MEMO_EDIT_TIMEZONE) == ESP_OK);
    memo_settings_edit_adjust(&edit, -1); assert(edit.value.tz_offset_min == -720);
    memo_settings_edit_cancel(&edit);

    assert(!memo_theme_dark(MEMO_THEME_AUTO, 7 * 3600, 0));
    assert(memo_theme_dark(MEMO_THEME_AUTO, 7 * 3600 - 1, 0));
    assert(!memo_theme_dark(MEMO_THEME_AUTO, 19 * 3600 - 1, 0));
    assert(memo_theme_dark(MEMO_THEME_AUTO, 19 * 3600, 0));
    assert(!memo_theme_dark(MEMO_THEME_AUTO, 23 * 3600, 480));
    assert(memo_theme_dark(MEMO_THEME_AUTO, 3600, -120));
    assert(memo_theme_dark(MEMO_THEME_AUTO, 0, 480));
    assert(!memo_theme_dark(MEMO_THEME_LIGHT, 0, 480));
    assert(memo_theme_dark(MEMO_THEME_DARK, 12 * 3600, 0));
    assert(memo_local_minutes(60, -720) == 721);
    assert(memo_local_minutes(23 * 3600 + 59 * 60, 840) == 839);
    puts("memo settings draft/cancel/save, NVS failure/rollback/retry, reboot and stored-value validation: PASS");
}
