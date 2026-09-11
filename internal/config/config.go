package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Feishu    FeishuConfig  `yaml:"feishu"`
	Engine    string        `yaml:"engine"`     // "pi" | "codebuddy"，默认 pi
	Pi        PiConfig      `yaml:"pi"`
	CodeBuddy CodeBuddyConfig `yaml:"codebuddy"`
	Sessions  SessionConfig `yaml:"sessions"`
}

type FeishuConfig struct {
	AppID           string `yaml:"app_id"`
	AppSecret       string `yaml:"app_secret"`
	BotName         string `yaml:"bot_name"`
	WsURL           string `yaml:"ws_url"`
	WSHealthTimeout int    `yaml:"ws_health_timeout"` // 秒；0 或不配置 = 默认 600，WS 僵死自愈阈值
}

type PiConfig struct {
	Binary   string `yaml:"binary"`
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
	Timeout  *int   `yaml:"timeout"` // seconds；nil=默认 1800，显式 0=不超时（空闲等待不中断）
}

// CodeBuddyConfig 是 codebuddy 引擎的配置。
type CodeBuddyConfig struct {
	Binary   string `yaml:"binary"`   // "codebuddy"
	Model    string `yaml:"model"`    // 如 "deepseek-v4.1-flash"
	APIKey   string `yaml:"api_key"`  // 非空时设置为 CODEBUDDY_API_KEY 环境变量
	Timeout  *int   `yaml:"timeout"`  // seconds；nil=默认 1800
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
	if c.Feishu.WSHealthTimeout <= 0 {
		c.Feishu.WSHealthTimeout = 600
	}
	// 引擎选择：默认 pi
	if c.Engine == "" {
		c.Engine = "pi"
	}
	// Pi 默认配置
	if c.Pi.Binary == "" {
		c.Pi.Binary = "pi"
	}
	if c.Pi.Provider == "" {
		c.Pi.Provider = "openai"
	}
	if c.Pi.Timeout == nil {
		def := 1800
		c.Pi.Timeout = &def
	}
	// CodeBuddy 默认配置
	if c.CodeBuddy.Binary == "" {
		c.CodeBuddy.Binary = "codebuddy"
	}
	if c.CodeBuddy.Timeout == nil {
		def := 1800
		c.CodeBuddy.Timeout = &def
	}
	if c.Sessions.DataDir == "" {
		c.Sessions.DataDir = "~/.feisuo/sessions"
	}
	if c.Sessions.DefaultName == "" {
		c.Sessions.DefaultName = "main"
	}
}

func expandHome(s string) string {
	if len(s) >= 2 && s[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, s[2:])
	}
	return s
}