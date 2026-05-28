package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level flagbase configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	IAM      IAMConfig      `yaml:"iam"`
	Storage  StorageConfig  `yaml:"storage"`
	Events   EventConfig    `yaml:"events"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type IAMConfig struct {
	JWTSecret string        `yaml:"jwt_secret"`
	TokenTTL  time.Duration `yaml:"token_ttl"`
}

type StorageConfig struct {
	BasePath string `yaml:"base_path"`
}

type EventConfig struct {
	NATSPort int `yaml:"nats_port"`
}

// Load reads config from path; falls back to defaults when path is empty.
func Load(path string) (*Config, error) {
	cfg := defaults()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
		},
		Database: DatabaseConfig{
			Path: "flagbase.db",
		},
		IAM: IAMConfig{
			JWTSecret: "change-me-in-production",
			TokenTTL:  24 * time.Hour,
		},
		Storage: StorageConfig{
			BasePath: "./data/storage",
		},
		Events: EventConfig{
			NATSPort: 4222,
		},
	}
}
