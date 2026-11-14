#pragma once

#define MEMO_PROVISION_LINE_SPACE (-8)

typedef struct {
    int text_x, text_width;
    int icon_x, icon_y, icon_size;
    int title_y, detail_y, detail_height;
    int identity_y, identity_height;
    int footer_y, footer_height;
} memo_provision_layout_t;

static inline memo_provision_layout_t memo_provision_layout(int width, int height,
                                                           int line_height)
{
    return (memo_provision_layout_t){
        .text_x = 24, .text_width = width - 48,
        .icon_x = (width - 40) / 2, .icon_y = 64, .icon_size = 40,
        .title_y = 108, .detail_y = 148,
        .detail_height = 2 * line_height + MEMO_PROVISION_LINE_SPACE,
        // Optical spacing includes the label font's 8 px top and 7 px bottom bearings.
        .identity_y = 211, .identity_height = 44,
        .footer_y = height - 57, .footer_height = line_height,
    };
}
