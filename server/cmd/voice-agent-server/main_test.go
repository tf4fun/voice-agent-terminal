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

// A second gateway that cannot listen must not run restart recovery against
// the database of the gateway that owns the port.
func TestBusyPortDoesNotRecoverActiveMessage(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	st, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	session, id := model.NewID(), model.NewID()
	if _, _, err := st.Send(ctx, session, id, "m", "scope", "", "in flight", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSessionReady(ctx, session); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx); err != nil {
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
	message, err := st.Message(ctx, session, id)
	if err != nil || message.State != "sending" {
		t.Fatalf("failed start modified the owner's message: %+v %v", message, err)
	}
}
