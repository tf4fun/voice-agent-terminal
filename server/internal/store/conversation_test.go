package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

func TestConversationPagesBothRolesWithoutGaps(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	read := st.Conversation
	empty, err := read(ctx, session, "tail", "")
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("empty page: %+v %v", empty, err)
	}
	revision := int64(1)
	inputs, replies := map[string]string{}, map[string]string{}
	for i := 0; i < 3; i++ {
		segment, id := model.NewID(), model.NewID()
		input := strings.Repeat("第一个问题你好", i*20+1)
		revision, err = st.AddSegment(ctx, session, segment, input, revision)
		if err != nil {
			t.Fatal(err)
		}
		message, err := st.Send(ctx, session, id, "model", "scope", revision)
		if err != nil {
			t.Fatal(err)
		}
		reply := strings.Repeat("这是助手的完整回复。", i*30+2)
		if err := st.ReplaceReply(ctx, id, reply); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMessageState(ctx, id, "completed", "stop", ""); err != nil {
			t.Fatal(err)
		}
		inputs[id], replies[id] = message.Input, reply
		current, err := st.Session(ctx, session)
		if err != nil {
			t.Fatal(err)
		}
		revision = current.Revision
	}
	tail, err := read(ctx, session, "tail", "")
	if err != nil || len(tail.Items) > 4 || tail.After != "" {
		t.Fatalf("tail: %+v %v", tail, err)
	}
	page := tail
	// Walk backward with one shared block, retaining exact UTF-8 boundaries.
	for rounds := 0; page.Before != ""; rounds++ {
		if rounds > 100 {
			t.Fatal("backward cursor loop")
		}
		older, err := read(ctx, session, "before", page.Before)
		if err != nil {
			t.Fatal(err)
		}
		if blockCursor(older.Items[len(older.Items)-1]) != blockCursor(page.Items[0]) {
			t.Fatal("missing backward overlap")
		}
		page = older
	}
	got := map[string]string{}
	for rounds := 0; ; rounds++ {
		if rounds > 100 {
			t.Fatal("forward cursor loop")
		}
		for i, block := range page.Items {
			if rounds > 0 && i == 0 {
				continue
			}
			if !utf8.ValidString(block.Text) || len(block.Text) > ConversationBytes {
				t.Fatal("invalid bounded text")
			}

			key := block.MessageID + block.Part
			if len(got[key]) != block.Offset {
				t.Fatalf("gap at %s: have %d want %d", key, len(got[key]), block.Offset)
			}
			got[key] += block.Text
		}
		if page.After == "" {
			break
		}
		newer, err := read(ctx, session, "after", page.After)
		if err != nil {
			t.Fatal(err)
		}
		if blockCursor(newer.Items[0]) != blockCursor(page.Items[len(page.Items)-1]) {
			t.Fatal("missing forward overlap")
		}
		page = newer
	}
	for id, text := range inputs {
		if got[id+"input"] != text {
			t.Fatal("input omitted or truncated")
		}
	}
	for id, text := range replies {
		if got[id+"reply"] != text {
			t.Fatal("reply omitted or truncated")
		}
	}
	if _, err := read(ctx, session, "from", "bad"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	other := model.NewID()
	if _, err := read(ctx, other, "from", tail.Start); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-session cursor accepted")
	}
}

func TestConversationRefreshesAnInitiallyEmptyReply(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	id := model.NewID()
	revision, err := st.AddSegment(ctx, session, model.NewID(), "我的问题", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Send(ctx, session, id, "model", "scope", revision); err != nil {
		t.Fatal(err)
	}
	page, err := st.Conversation(ctx, session, "tail", "")
	if err != nil || len(page.Items) != 2 || page.Items[0].Part != "input" || page.Items[1].Part != "reply" {
		t.Fatalf("pending roles: %+v %v", page, err)
	}
	if err := st.ReplaceReply(ctx, id, "正在回复"); err != nil {
		t.Fatal(err)
	}
	refreshed, err := st.Conversation(ctx, session, "from", page.Start)
	if err != nil || refreshed.Items[1].Text != "正在回复" {
		t.Fatalf("reply did not grow: %+v %v", refreshed, err)
	}
}
