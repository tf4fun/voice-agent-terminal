package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/tf4fun/voice-agent-terminal/server/internal/model"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

func TestBusyPortDoesNotRecoverActiveRecording(t *testing.T) {
	for _, state := range []string{"capturing", "staged"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			st, err := store.Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			session, id := model.NewID(), model.NewID()
			if _, err := st.BeginSession(ctx, session, "active owner"); err != nil {
				t.Fatal(err)
			}
			if err := st.SetSessionReady(ctx, session); err != nil {
				t.Fatal(err)
			}
			revision, err := st.AddSegment(ctx, session, id, "", 1)
			if err != nil {
				t.Fatal(err)
			}
			generation, err := st.BeginCaptureMode(ctx, session, id, revision, store.CaptureStaged)
			if err != nil {
				t.Fatal(err)
			}
			transcriptState := "capturing"
			if state == "staged" {
				transcriptState = "ready"
			}
			revision, err = st.UpdateTranscript(ctx, session, id, generation, "in flight", transcriptState)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			t.Setenv("VOICE_DATA_DIR", directory)
			t.Setenv("VOICE_MDNS", "false")
			err = run([]string{"-env", filepath.Join(t.TempDir(), "missing.env"), "-addr", listener.Addr().String()})
			var networkError *net.OpError
			if !errors.As(err, &networkError) || networkError.Op != "listen" {
				t.Fatalf("expected occupied address, got %v", err)
			}
			segment, observed, err := st.DraftSegment(ctx, session, id)
			if err != nil || segment.State != state || segment.Text != "in flight" || segment.Generation != generation || observed != revision {
				t.Fatalf("failed start modified owner's recording: %+v revision=%d err=%v", segment, observed, err)
			}
		})
	}
}
