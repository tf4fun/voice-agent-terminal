package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigurationWithoutCredentialsIsInspectable(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil || cfg.OpenAIModel != "gpt-live-transcribe" || cfg.Workers != 2 || cfg.HermesPrompt != DefaultSystemPrompt {
		t.Fatalf("%+v %v", cfg, err)
	}
	for _, name := range []string{"VOICE_CHAT_WORKERS", "VOICE_ASR_PROVIDER", "VOICE_ADDR", "HERMES_BASE_URL", "OPENAI_REALTIME_URL"} {
		_, err := Load(func(key string) string {
			if key == name {
				return "invalid"
			}
			return ""
		})
		if err == nil {
			t.Fatalf("accepted invalid %s", name)
		}
	}
}

func TestEnvironmentValuesAreLiteralAndProcessWins(t *testing.T) {
	t.Setenv("VOICE_ADDR", "127.0.0.1:9999")
	t.Setenv("VOICE_TEST_LITERAL", "")
	_ = os.Unsetenv("VOICE_TEST_LITERAL")
	path := filepath.Join(t.TempDir(), ".env.local")
	if err := os.WriteFile(path, []byte("VOICE_ADDR=:8080\nVOICE_TEST_LITERAL='$(never-run) `literal`'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("VOICE_ADDR") != "127.0.0.1:9999" || os.Getenv("VOICE_TEST_LITERAL") != "$(never-run) `literal`" {
		t.Fatal("environment precedence or literal values changed")
	}
}

func TestCaptureRequestBudgetPreservesAudioLimit(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CaptureTimeout != 5*time.Minute || cfg.CaptureRequestTimeout() != 6*time.Minute {
		t.Fatalf("audio=%v request=%v", cfg.CaptureTimeout, cfg.CaptureRequestTimeout())
	}
}

func TestSpeechShapingSettings(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SpeechHighPassHz != 200 || cfg.SpeechGainDB != 0 || cfg.SpeechCeilingDB != -13.5 {
		t.Fatalf("defaults %v %v %v", cfg.SpeechHighPassHz, cfg.SpeechGainDB, cfg.SpeechCeilingDB)
	}
	cfg, err = Load(env(map[string]string{"VOICE_SPEECH_HIGHPASS_HZ": "0", "VOICE_SPEECH_GAIN_DB": "4.5", "VOICE_SPEECH_CEILING_DB": "-10"}))
	if err != nil || cfg.SpeechHighPassHz != 0 || cfg.SpeechGainDB != 4.5 || cfg.SpeechCeilingDB != -10 {
		t.Fatalf("parsed %+v %v", cfg, err)
	}
	for key, bad := range map[string]string{
		"VOICE_SPEECH_HIGHPASS_HZ": "-1", "VOICE_SPEECH_GAIN_DB": "21", "VOICE_SPEECH_CEILING_DB": "1", "VOICE_SPEECH_GAIN_DB ": "x",
	} {
		if _, err := Load(env(map[string]string{strings.TrimSpace(key): bad})); err == nil {
			t.Fatalf("%s=%s accepted", key, bad)
		}
	}
}
