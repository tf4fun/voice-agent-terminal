package store

import (
	"context"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
)

// The device polls the turn every second while a reply is written; the query
// reports sizes without reading the text out of SQLite.
func TestTurnReadsMetadataOnly(t *testing.T) {
	ctx := context.Background()
	st, session := fixture(t)
	id := model.NewID()
	if _, _, err := st.Send(ctx, session, id, "model", "scope", "", "你好", false); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"latest", "message"} {
		turn, err := st.Turn(ctx, session, mode, id)
		if err != nil || turn.Message == nil {
			t.Fatal(mode, turn, err)
		}
		got := turn.Message
		if got.ID != id || got.Input != "" || got.InputBytes != len("你好") || got.ReplyBytes != 0 || got.State != "queued" || got.Revision != 1 {
			t.Fatalf("%s: %+v", mode, *got)
		}
	}
}
