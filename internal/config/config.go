package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type HttpListenConfig struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type DatabaseConfig struct {
	Hostname           string `json:"hostname"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password"`
	Database           string `json:"database"`
	EncryptionKey      string `json:"encryption_key"`
	MaxIdleConnections int    `json:"max_idle_connections"`
	MaxOpenConnections int    `json:"max_open_connections"`
	SSLModeOverride    string `json:"ssl_mode"`
}

type SecurityConfig struct {
	IdentityService string `json:"identity_service"`
}

type ExecutorConfig struct {
	Bucket   string `json:"bucket"`
	Path     string `json:"path"`
	Filename string `json:"filename"`
}

type Config struct {
	HttpListenConfig HttpListenConfig `json:"http"`
	Database         *DatabaseConfig  `json:"database"`
	Executor         *ExecutorConfig  `json:"executor"`
	Security         *SecurityConfig  `json:"security"`
}

func LoadConfig(path string) (*Config, error) {
	filePath := filepath.Join(".", filepath.Clean(path))
	b, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var config Config

	err = json.Unmarshal(b, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}
