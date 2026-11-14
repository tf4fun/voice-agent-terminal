#include <assert.h>
#include <stdio.h>
#include "memo_power.h"

int main(void)
{
    memo_power_t p={.activity_ms=1000};
    assert(memo_power_poll(&p,30999,30,5,false,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,31000,30,5,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_key(&p,32000,true,false)); // Wake press never records.
    assert(!p.blank);
    assert(memo_power_key(&p,32500,false,false)); // LONG
    assert(memo_power_key(&p,32600,false,true)); // RELEASE
    assert(memo_power_key(&p,32600,false,false)); // trailing CLICK
    assert(!memo_power_key(&p,33000,true,false)); // New gesture operates normally.
    assert(!memo_power_key(&p,33100,false,true));
    assert(memo_power_poll(&p,633100,30,5,true,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,663099,30,5,false,true)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,663100,30,5,false,true)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,1233100,30,5,false,true)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,1233100,30,5,false,false)==MEMO_POWER_SLEEP);
    assert(memo_power_poll(&p,1233100,0,0,false,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,1233100,30,0,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,1233100,0,5,false,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,1,30,5,false,false)==MEMO_POWER_ON); // Clock reset.
    assert(p.activity_ms==1);
    p=(memo_power_t){0};
    assert(memo_power_poll(&p,29999,30,5,false,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,30000,30,5,false,false)==MEMO_POWER_BLANK);
    assert(p.blank_since_ms==30000);
    assert(memo_power_poll(&p,329999,30,5,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,330000,30,5,false,false)==MEMO_POWER_SLEEP);
    // A shorter sleep setting cannot bypass the display-on interval.
    p=(memo_power_t){0};
    assert(memo_power_poll(&p,179999,180,1,false,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,180000,180,1,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,239999,180,1,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,240000,180,1,false,false)==MEMO_POWER_SLEEP);
    // Delayed polling starts the blank interval when the display really turns off.
    p=(memo_power_t){0};
    assert(memo_power_poll(&p,900000,30,1,false,false)==MEMO_POWER_BLANK);
    assert(p.blank_since_ms==900000);
    assert(memo_power_poll(&p,959999,30,1,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,960000,30,1,false,false)==MEMO_POWER_SLEEP);
    assert(memo_power_key(&p,961000,true,false) && !p.blank && p.blank_since_ms==0);
    assert(memo_power_poll(&p,991000,30,1,false,false)==MEMO_POWER_BLANK);
    assert(p.blank_since_ms==991000);
    assert(memo_power_poll(&p,1050999,30,1,false,false)==MEMO_POWER_BLANK);
    assert(memo_power_poll(&p,1051000,30,1,false,false)==MEMO_POWER_SLEEP);
    assert(memo_power_poll(&p,1051000,0,1,false,false)==MEMO_POWER_ON);
    assert(memo_power_poll(&p,9999999,0,1,false,false)==MEMO_POWER_ON);
    puts("PASS: idle blanking, sleep delay after blanking and complete wake gestures");
    return 0;
}
