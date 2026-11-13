package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationWithoutCredentialsIsInspectable(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil || cfg.OpenAIModel != "gpt-live-transcribe" || cfg.Workers != 2 {
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
