package main

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type genrocConfig struct {
	Server string `yaml:"server,omitempty"`
}

func configFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "genroc", "config.yaml"), nil
}

func loadConfig() genrocConfig {
	path, err := configFilePath()
	if err != nil {
		return genrocConfig{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return genrocConfig{}
	}
	var cfg genrocConfig
	yaml.Unmarshal(data, &cfg)
	return cfg
}

func saveConfig(cfg genrocConfig) error {
	path, err := configFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// ── last-instance state (genctl run → @last) ───────────────────────────────────

// lastInstanceFilePath is where `run` records the most recently started instance
// id, kept beside the config so a follow-up command can resolve `@last` (or a bare
// default) without the caller copy-pasting it.
func lastInstanceFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "genroc", "last"), nil
}

// saveLastInstance records id as the most recently started instance. Best-effort:
// the caller treats a failure as non-fatal so `run` still succeeds.
func saveLastInstance(id string) error {
	path, err := lastInstanceFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id+"\n"), 0600)
}

// loadLastInstance returns the most recently started instance id, or "" if none
// has been recorded yet.
func loadLastInstance() string {
	path, err := lastInstanceFilePath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveInstanceID maps an instance-id argument to a concrete id. The id must be
// given explicitly: the literal "@last" resolves to the most recently started
// instance (recorded by `run`), and any other non-empty value is returned
// unchanged. An empty argument is an error — a bare command never implies @last —
// as is "@last" when nothing has been started yet.
func resolveInstanceID(arg string) string {
	if arg == "" {
		fatal("an instance id is required — pass one explicitly, or @last for the most recently started instance")
	}
	if arg != "@last" {
		return arg
	}
	id := loadLastInstance()
	if id == "" {
		fatal("@last: no instance recorded yet — run `genctl run <process>` first")
	}
	return id
}
