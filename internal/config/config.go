package config

import (
	goconfig "github.com/flomation-co/go-config"

	"flomation.app/automate/api/internal/mtls"
)

type HttpListenConfig struct {
	Address string `json:"address" env:"LISTEN_ADDRESS" arg:"listen-address"`
	Port    int    `json:"port" env:"LISTEN_PORT" arg:"listen-port"`
}

type DatabaseConfig struct {
	Hostname           string `json:"hostname" env:"DATABASE_HOSTNAME" arg:"database-hostname"`
	Port               int    `json:"port" env:"DATABASE_PORT" arg:"database-port"`
	Username           string `json:"username" env:"DATABASE_USER" arg:"database-user"`
	Password           string `json:"password" env:"DATABASE_PASSWORD" arg:"database-password"`
	Database           string `json:"database" env:"DATABASE_NAME" arg:"database-name"`
	EncryptionKey      string `json:"encryption_key" env:"DATABASE_ENCRYPTION_KEY" arg:"database-encryption-key"`
	MaxIdleConnections int    `json:"max_idle_connections" env:"DATABASE_MAX_IDLE_CONNS" arg:"database-max-idle-connections"`
	MaxOpenConnections int    `json:"max_open_connections" env:"DATABASE_MAX_OPEN_CONNS" arg:"database-max-open-connections"`
	SSLModeOverride    string `json:"ssl_mode" env:"DATABASE_SSL_MODE" arg:"database-ssl-mode"`
}

type SecurityConfig struct {
	IdentityService string `json:"identity_service" env:"IDENTITY_SERVICE" arg:"identity-service"`
	AllowedOrigins  string `json:"allowed_origins" env:"ALLOWED_ORIGINS" arg:"allowed-origins"`
}

type LaunchConfig struct {
	URL         string `json:"url" env:"LAUNCH_SERVICE_URL" arg:"launch-service-url"`
	InternalURL string `json:"internal_url,omitempty" env:"LAUNCH_INTERNAL_URL" arg:"launch-internal-url"`
	PublicURL   string `json:"public_url" env:"LAUNCH_PUBLIC_URL" arg:"launch-public-url"`
	APIURL      string `json:"api_url" env:"API_PUBLIC_URL" arg:"api-public-url"`
}

// InternalLaunchURL returns the internal mTLS URL for Launch if configured,
// otherwise falls back to the public Launch URL.
func (c *Config) InternalLaunchURL() string {
	if c.Launch.InternalURL != "" {
		return c.Launch.InternalURL
	}
	return c.Launch.URL
}

type SMTPConfig struct {
	Host     string `json:"host" env:"SMTP_HOST" arg:"smtp-host"`
	Port     int    `json:"port" env:"SMTP_PORT" arg:"smtp-port"`
	Username string `json:"username" env:"SMTP_USERNAME" arg:"smtp-username"`
	Password string `json:"password" env:"SMTP_PASSWORD" arg:"smtp-password"`
	From     string `json:"from" env:"SMTP_FROM" arg:"smtp-from"`
}

type EmbeddingConfig struct {
	Enabled         bool   `json:"enabled" env:"EMBEDDING_ENABLED" arg:"embedding-enabled"`
	Region          string `json:"region" env:"EMBEDDING_REGION" arg:"embedding-region"`
	ModelID         string `json:"model_id" env:"EMBEDDING_MODEL_ID" arg:"embedding-model-id"`
	Dimensions      int    `json:"dimensions" env:"EMBEDDING_DIMENSIONS" arg:"embedding-dimensions"`
	TopK            int    `json:"top_k" env:"EMBEDDING_TOP_K" arg:"embedding-top-k"`
	AccessKeyID     string `json:"access_key_id" env:"EMBEDDING_ACCESS_KEY_ID" arg:"embedding-access-key-id"`
	SecretAccessKey string `json:"secret_access_key" env:"EMBEDDING_SECRET_ACCESS_KEY" arg:"embedding-secret-access-key"`
}

// MetricsConfig controls the Prometheus /metrics endpoint.
type MetricsConfig struct {
	Enabled    bool     `json:"enabled" env:"METRICS_ENABLED" arg:"metrics-enabled"`
	AllowedIPs []string `json:"allowed_ips"`
}

type BillingConfig struct {
	QuotaEnforcementEnabled bool   `json:"quota_enforcement_enabled" env:"QUOTA_ENFORCEMENT_ENABLED" arg:"quota-enforcement-enabled"`
	InternalURL             string `json:"internal_url,omitempty" env:"BILLING_INTERNAL_URL" arg:"billing-internal-url"`
}

// OAuthProviderConfig holds client credentials for a default OAuth provider.
type OAuthProviderConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type Config struct {
	HttpListenConfig HttpListenConfig `json:"http"`
	Database         DatabaseConfig   `json:"database"`
	Security         SecurityConfig   `json:"security"`
	Launch           LaunchConfig     `json:"launch"`
	SMTP             SMTPConfig       `json:"smtp"`
	Embedding        *EmbeddingConfig `json:"embedding,omitempty"`
	TLS              *mtls.TLSConfig  `json:"tls,omitempty"`
	Metrics          MetricsConfig    `json:"metrics"`
	Billing          BillingConfig    `json:"billing"`
	OAuth            map[string]OAuthProviderConfig `json:"oauth,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	var c Config
	if err := goconfig.Load(&c, goconfig.String(path)); err != nil {
		return nil, err
	}

	return &c, nil
}
