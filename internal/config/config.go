package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	PollInterval  time.Duration `yaml:"poll_interval"`
	ReadyStatuses []string      `yaml:"ready_statuses"`
	QAStatuses    []string      `yaml:"qa_statuses"`
}

func Default() Config {
	return Config{PollInterval: 60 * time.Second, ReadyStatuses: []string{"QA Approved"}, QAStatuses: []string{"Ready for QA"}}
}

// Dir returns the config directory, honoring CODESTATTUI_HOME.
func Dir() string {
	if d := os.Getenv("CODESTATTUI_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "codestattui")
}

func TasksPath() string { return filepath.Join(Dir(), "tasks.yaml") }

// ArchivePath is where archived tasks go, kept apart from tasks.yaml so the active list stays small.
func ArchivePath() string { return filepath.Join(Dir(), "archive.yaml") }

// Load reads config.yaml, falling back to defaults for missing file or fields.
func Load() (Config, error) {
	c := Default()
	b, err := os.ReadFile(filepath.Join(Dir(), "config.yaml"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Default(), err
	}
	if c.PollInterval < 5*time.Second {
		c.PollInterval = 5 * time.Second
	}
	if len(c.ReadyStatuses) == 0 {
		c.ReadyStatuses = Default().ReadyStatuses
	}
	if len(c.QAStatuses) == 0 {
		c.QAStatuses = Default().QAStatuses
	}
	return c, nil
}
