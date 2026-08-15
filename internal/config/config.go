package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu   FeishuConfig   `yaml:"feishu"`
	Pi       PiConfig       `yaml:"pi"`
	Sessions SessionConfig  `yaml:"sessions"`
	Intent   IntentConfig   `yaml:"intent"`
}

type IntentConfig struct {
	Enabled bool   `yaml:"enabled"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Timeout int    `yaml:"timeout"` // seconds
}

type FeishuConfig struct {
	AppID     string `yaml:"app_id"`
	AppSecret string `yaml:"app_secret"`
	BotName   string `yaml:"bot_name"`
	WsURL     string `yaml:"ws_url"`
}

type PiConfig struct {
	Binary  string `yaml:"binary"`
	Provider string `yaml:"provider"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Timeout int    `yaml:"timeout"` // seconds
}

type SessionConfig struct {
	DataDir    string `yaml:"data_dir"`
	DefaultName string `yaml:"default_name"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	applyDefaults(&cfg)
	// expand home dir
	cfg.Sessions.DataDir = expandHome(cfg.Sessions.DataDir)
	os.MkdirAll(cfg.Sessions.DataDir, 0755)
	return cfg, nil
}

func applyDefaults(c *Config) {
	if c.Feishu.WsURL == "" {
		c.Feishu.WsURL = "wss://open.feishu.cn/ws/v1/ws/"
	}
	if c.Pi.Binary == "" {
		c.Pi.Binary = "pi"
	}
	if c.Pi.Provider == "" {
		c.Pi.Provider = "openai"
	}
	if c.Pi.Timeout == 0 {
		c.Pi.Timeout = 300
	}
	if c.Sessions.DataDir == "" {
		c.Sessions.DataDir = "~/.feisuo/sessions"
	}
	if c.Sessions.DefaultName == "" {
		c.Sessions.DefaultName = "main"
	}
	if c.Intent.Model == "" {
		c.Intent.Model = "glm4:9b"
	}
	if c.Intent.BaseURL == "" {
		c.Intent.BaseURL = "http://127.0.0.1:3002/v1"
	}
	if c.Intent.Timeout == 0 {
		c.Intent.Timeout = 8
	}
}

func expandHome(s string) string {
	if len(s) >= 2 && s[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, s[2:])
	}
	return s
}