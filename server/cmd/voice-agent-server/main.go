package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tf4fun/voice-agent-terminal/server/internal/asr"
	"github.com/tf4fun/voice-agent-terminal/server/internal/config"
	"github.com/tf4fun/voice-agent-terminal/server/internal/discovery"
	"github.com/tf4fun/voice-agent-terminal/server/internal/gateway"
	"github.com/tf4fun/voice-agent-terminal/server/internal/hermes"
	"github.com/tf4fun/voice-agent-terminal/server/internal/httpapi"
	"github.com/tf4fun/voice-agent-terminal/server/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "voice-agent-server:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 1 && arguments[0] == "version" {
		fmt.Println(httpapi.Version)
		return nil
	}
	flags := flag.NewFlagSet("voice-agent-server", flag.ContinueOnError)
	environment := flags.String("env", ".env.local", "literal environment file; process variables take precedence")
	address := flags.String("addr", "", "override VOICE_ADDR")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if err := config.LoadEnvFile(*environment); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if *address != "" {
		if _, _, err := net.SplitHostPort(*address); err != nil {
			return fmt.Errorf("invalid listen address")
		}
		cfg.Addr = *address
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Recover(context.Background()); err != nil {
		return err
	}
	var transcriber asr.Transcriber = &asr.OpenAI{URL: cfg.OpenAIURL, APIKey: cfg.OpenAIKey, Model: cfg.OpenAIModel}
	if cfg.ASRProvider == "fake" {
		transcriber = &asr.Fake{}
	}
	client := &hermes.Client{BaseURL: cfg.HermesURL, APIKey: cfg.HermesKey}
	service := gateway.New(st, cfg, client, transcriber, logger)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	service.Start(ctx)
	defer service.Close()
	var publisher *discovery.Publisher
	if cfg.MDNS && shouldAdvertiseLAN(cfg.Addr) {
		_, portText, _ := net.SplitHostPort(listener.Addr().String())
		port, _ := strconv.Atoi(portText)
		publisher, err = discovery.Advertise(cfg.MDNSName, port, cfg.MDNSInterface, logger)
		if err != nil {
			logger.Warn("mDNS advertisement unavailable")
		}
	}
	defer publisher.Close()
	server := &http.Server{Handler: httpapi.New(service), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	logger.Info("listening", "addr", listener.Addr().String(), "version", httpapi.Version, "asr_provider", cfg.ASRProvider)
	select {
	case err := <-finished:
		cancel()
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}

func shouldAdvertiseLAN(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return false
	}
	if zone := strings.LastIndex(host, "%"); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}
