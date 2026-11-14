#pragma once

#include "agent_status.h"
#include "lvgl.h"

// Application-owned widget; callers hold the LVGL lock for its entire lifecycle.
void agent_ui_status_build(lv_obj_t *parent);
void agent_ui_status_refresh(agent_status_t status, const char *notice);
void agent_ui_status_destroy(void);
