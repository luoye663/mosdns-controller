// Package config loads the controller configuration before any listener starts.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		PublicListen   string   `yaml:"public_listen"`
		InternalListen string   `yaml:"internal_listen"`
		TrustedProxies []string `yaml:"trusted_proxies"`
	} `yaml:"server"`
	Storage struct {
		Path string `yaml:"path"`
	} `yaml:"storage"`
	Mosdns struct {
		BaseURL   string `yaml:"base_url"`
		TokenFile string `yaml:"token_file"`
	} `yaml:"mosdns"`
	Web struct {
		SecureCookie  bool          `yaml:"secure_cookie"`
		SessionTTL    time.Duration `yaml:"-"`
		SessionTTLRaw string        `yaml:"session_ttl"`
	} `yaml:"web"`
	HTTP struct {
		MaxBodyBytes int64         `yaml:"max_body_bytes"`
		Timeout      time.Duration `yaml:"-"`
		TimeoutRaw   string        `yaml:"timeout"`
	} `yaml:"http"`
}

func Default() Config {
	var c Config
	c.Server.PublicListen, c.Server.InternalListen = "0.0.0.0:8080", "0.0.0.0:8081"
	c.Storage.Path = "/var/lib/mosdns-controller/controller.db"
	c.Mosdns.BaseURL, c.Mosdns.TokenFile = "http://mosdns:9091", "/run/secrets/mosdns_control_token"
	c.Web.SessionTTLRaw, c.HTTP.MaxBodyBytes, c.HTTP.TimeoutRaw = "24h", 64<<20, "30s"
	return c
}

// Load uses a strict YAML decoder so misspelled security options cannot be ignored.
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		decoder := yaml.NewDecoder(strings.NewReader(string(data)))
		decoder.KnownFields(true)
		if err := decoder.Decode(&c); err != nil {
			return Config{}, fmt.Errorf("decode config: %w", err)
		}
	}
	if err := applyEnv(&c); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func applyEnv(c *Config) error {
	setString("CONTROLLER_PUBLIC_LISTEN", &c.Server.PublicListen)
	setString("CONTROLLER_INTERNAL_LISTEN", &c.Server.InternalListen)
	setString("CONTROLLER_STORAGE_PATH", &c.Storage.Path)
	setString("CONTROLLER_MOSDNS_BASE_URL", &c.Mosdns.BaseURL)
	setString("CONTROLLER_MOSDNS_TOKEN_FILE", &c.Mosdns.TokenFile)
	setString("CONTROLLER_SESSION_TTL", &c.Web.SessionTTLRaw)
	setString("CONTROLLER_HTTP_TIMEOUT", &c.HTTP.TimeoutRaw)
	if err := setBool("CONTROLLER_SECURE_COOKIE", &c.Web.SecureCookie); err != nil {
		return err
	}
	if raw := os.Getenv("CONTROLLER_MAX_BODY_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("CONTROLLER_MAX_BODY_BYTES: %w", err)
		}
		c.HTTP.MaxBodyBytes = n
	}
	return nil
}
func setString(name string, target *string) {
	if value := os.Getenv(name); value != "" {
		*target = value
	}
}
func setBool(name string, target *bool) error {
	if value := os.Getenv(name); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*target = parsed
	}
	return nil
}

func (c *Config) Validate() error {
	for _, address := range []string{c.Server.PublicListen, c.Server.InternalListen} {
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("invalid listen address %q: %w", address, err)
		}
	}
	if c.Server.PublicListen == c.Server.InternalListen {
		return errors.New("public and internal listeners must differ")
	}
	if strings.TrimSpace(c.Storage.Path) == "" {
		return errors.New("storage.path is required")
	}
	parsedURL, err := url.ParseRequestURI(c.Mosdns.BaseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return errors.New("mosdns.base_url must be an absolute URL")
	}
	if c.Web.SessionTTL, err = time.ParseDuration(c.Web.SessionTTLRaw); err != nil || c.Web.SessionTTL <= 0 {
		return errors.New("web.session_ttl must be a positive duration")
	}
	if c.HTTP.Timeout, err = time.ParseDuration(c.HTTP.TimeoutRaw); err != nil || c.HTTP.Timeout <= 0 {
		return errors.New("http.timeout must be a positive duration")
	}
	if c.HTTP.MaxBodyBytes <= 0 || c.HTTP.MaxBodyBytes > 64<<20 {
		return errors.New("http.max_body_bytes must be between 1 and 67108864")
	}
	return nil
}
