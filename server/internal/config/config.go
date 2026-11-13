package config

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr           string
	DataDir        string
	APIKey         string
	MDNS           bool
	MDNSName       string
	MDNSInterface  string
	HermesURL      string
	HermesKey      string
	HermesModel    string
	ASRProvider    string
	OpenAIKey      string
	OpenAIURL      string
	OpenAIModel    string
	Workers        int
	ChatTimeout    time.Duration
	CaptureTimeout time.Duration
	MaxAudioBytes  int64
}

func Load(getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if text := strings.TrimSpace(getenv(key)); text != "" {
			return text
		}
		return fallback
	}
	cfg := Config{
		Addr: value("VOICE_ADDR", ":8080"), DataDir: value("VOICE_DATA_DIR", "./data"),
		APIKey: getenv("VOICE_API_KEY"), MDNSName: value("VOICE_MDNS_NAME", "VoiceAgent"),
		MDNSInterface: value("VOICE_MDNS_INTERFACE", ""),
		HermesURL:     strings.TrimRight(value("HERMES_BASE_URL", "http://127.0.0.1:8642"), "/"),
		HermesKey:     getenv("HERMES_API_KEY"), HermesModel: value("HERMES_MODEL", "hermes-agent"),
		ASRProvider: value("VOICE_ASR_PROVIDER", "openai"), OpenAIKey: getenv("OPENAI_API_KEY"),
		OpenAIURL:   value("OPENAI_REALTIME_URL", "wss://api.openai.com/v1/realtime?intent=transcription"),
		OpenAIModel: value("OPENAI_TRANSCRIBE_MODEL", "gpt-live-transcribe"),
		ChatTimeout: 10 * time.Minute, CaptureTimeout: 5 * time.Minute, MaxAudioBytes: 15 << 20,
	}
	var err error
	if cfg.MDNS, err = strconv.ParseBool(value("VOICE_MDNS", "true")); err != nil {
		return Config{}, fmt.Errorf("invalid VOICE_MDNS")
	}
	if cfg.Workers, err = strconv.Atoi(value("VOICE_CHAT_WORKERS", "2")); err != nil || cfg.Workers < 1 || cfg.Workers > 8 {
		return Config{}, fmt.Errorf("VOICE_CHAT_WORKERS must be between 1 and 8")
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return Config{}, fmt.Errorf("VOICE_ADDR must be host:port")
	}
	for _, target := range []struct {
		name, value string
		websocket   bool
	}{
		{"HERMES_BASE_URL", cfg.HermesURL, false}, {"OPENAI_REALTIME_URL", cfg.OpenAIURL, true},
	} {
		parsed, err := url.Parse(target.value)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return Config{}, fmt.Errorf("invalid %s", target.name)
		}
		valid := parsed.Scheme == "http" || parsed.Scheme == "https"
		if target.websocket {
			valid = parsed.Scheme == "ws" || parsed.Scheme == "wss"
		}
		if !valid {
			return Config{}, fmt.Errorf("invalid %s scheme", target.name)
		}
	}
	if cfg.ASRProvider != "openai" && cfg.ASRProvider != "fake" {
		return Config{}, fmt.Errorf("VOICE_ASR_PROVIDER must be openai or fake")
	}
	return cfg, nil
}

// LoadEnvFile treats values literally and preserves variables already in the environment.
func LoadEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, found := strings.Cut(text, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !found || key == "" {
			return fmt.Errorf("invalid environment entry on line %d", line)
		}
		for index, ch := range key {
			if !(ch >= 'A' && ch <= 'Z' || ch == '_' || index > 0 && ch >= '0' && ch <= '9') {
				return fmt.Errorf("invalid environment name on line %d", line)
			}
		}
		if len(value) >= 2 && (value[0] == '\'' && value[len(value)-1] == '\'' || value[0] == '"' && value[len(value)-1] == '"') {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set environment entry on line %d", line)
			}
		}
	}
	return scanner.Err()
}
