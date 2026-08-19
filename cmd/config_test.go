package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/miradorlabs/mirador-cli/internal/config"
)

// TestConfigView_NeverCarriesTheAPIKey guards a leak that shipped once: `config show
// -o json` serialized the internal Config, which holds MIRADOR_API_KEY, printing a
// live server key into whatever consumed the output.
func TestConfigView_NeverCarriesTheAPIKey(t *testing.T) {
	view := configView{
		Profile: "default",
		APIURL:  "https://api.example",
		Auth:    "server key (MIRADOR_API_KEY)",
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "mir_srv_") {
		t.Fatalf("configView serialized a credential: %s", encoded)
	}
	if !strings.Contains(string(encoded), "server key") {
		t.Errorf("the view should still report which credential type is in use: %s", encoded)
	}
}

// TestConfig_APIKeyIsNotSerializable is the second layer: even if a future command
// renders config.Config directly, the key must not travel with it.
func TestConfig_APIKeyIsNotSerializable(t *testing.T) {
	encoded, err := json.Marshal(&config.Config{
		ProfileName: "default",
		APIKey:      "mir_srv_secret",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "mir_srv_secret") {
		t.Fatalf("config.Config serialized the API key: %s", encoded)
	}
}
