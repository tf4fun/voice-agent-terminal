#include <assert.h>
#include <stdio.h>
#include "memo_key.h"

int main(void)
{
    memo_key_t key = {0};
    const int64_t hold_us = 500000;
    assert(memo_key_release(&key, 0, hold_us) == MEMO_KEY_NONE);
    for (int i = 0; i < 5; i++) {
        const int64_t at = 1000000 + i * 40000;
        memo_key_press(&key, at, true);
        assert(memo_key_release(&key, at + 20000, hold_us) == MEMO_KEY_CLICK);
        assert(memo_key_release(&key, at + 20001, hold_us) == MEMO_KEY_NONE);
        assert(memo_key_hold(&key, at + hold_us, hold_us, true) == MEMO_KEY_NONE);
    }
    memo_key_press(&key, 2000000, true);
    assert(memo_key_hold(&key, 2499999, hold_us, false) == MEMO_KEY_NONE);
    assert(memo_key_hold(&key, 2500000, hold_us, false) == MEMO_KEY_HOLD);
    assert(memo_key_hold(&key, 2600000, hold_us, true) == MEMO_KEY_NONE);
    assert(memo_key_release(&key, 2700000, hold_us) == MEMO_KEY_NONE);

    // A release queued behind work still emits the missed hold exactly once.
    memo_key_press(&key, 3000000, true);
    assert(memo_key_release(&key, 3500000, hold_us) == MEMO_KEY_HOLD);
    assert(memo_key_release(&key, 3500001, hold_us) == MEMO_KEY_NONE);
    memo_key_press(&key, 4000000, true);
    assert(memo_key_release(&key, 4499999, hold_us) == MEMO_KEY_CLICK);

    // A consumed wake gesture cannot turn into a click or a polled hold.
    memo_key_press(&key, 5000000, false);
    assert(memo_key_hold(&key, 5600000, hold_us, true) == MEMO_KEY_NONE);
    assert(memo_key_release(&key, 5700000, hold_us) == MEMO_KEY_NONE);
    memo_key_press(&key, 6000000, true);
    assert(memo_key_release(&key, 6010000, hold_us) == MEMO_KEY_CLICK);
    puts("memo per-press immediate clicks, hold boundary and wake suppression: PASS");
}
