package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/oarw/dingzi/internal/server"
	"golang.org/x/crypto/bcrypt"
)

func TestMissingPasswordHashResetsOnlyAdminPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfigAtomic(path, panelConfig{AgentSecret: "existing-registration-key", GitHub: &server.GitHubConfig{
		ClientID: "fixture-client", ClientSecret: "fixture-secret", CallbackURL: "https://panel.example/auth/github/callback", AllowedUserIDs: []int64{42},
	}}); err != nil {
		t.Fatal(err)
	}
	cfg, generated, password, err := loadOrInitConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !generated || password == "" || cfg.AgentSecret != "existing-registration-key" || cfg.GitHub == nil || cfg.GitHub.ClientSecret != "fixture-secret" || !reflect.DeepEqual(cfg.GitHub.AllowedUserIDs, []int64{42}) {
		t.Fatal("clearing password_hash did not reset the password while preserving the agent key")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(cfg.PasswordHash), []byte(password)); err != nil {
		t.Fatal("generated password does not match its persisted hash")
	}
	again, generatedAgain, passwordAgain, err := loadOrInitConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if generatedAgain || passwordAgain != "" || !reflect.DeepEqual(again, cfg) {
		t.Fatal("restart regenerated valid credentials")
	}
}

func TestInvalidPasswordHashFailsWithoutReplacingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeConfigAtomic(path, panelConfig{AgentSecret: "existing-registration-key", PasswordHash: "not-a-bcrypt-hash"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadOrInitConfig(path); err == nil {
		t.Fatal("invalid password hash allowed a panel with no working login to start")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("invalid configuration was overwritten")
	}
}
