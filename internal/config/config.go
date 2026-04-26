package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the runtime configuration loaded from YAML + env.
type Config struct {
	Listen        string   `yaml:"listen"`         // e.g. "0.0.0.0:8080"
	PublicBaseURL string   `yaml:"public_base_url"` // e.g. "http://1.2.3.4:8080" (optional, used in UI hints)
	DatabasePath  string   `yaml:"database_path"`  // e.g. "./data/portly.db"
	SessionSecret string   `yaml:"session_secret"` // random hex string; auto-generated if empty
	Admin         Admin    `yaml:"admin"`
	Security      Security `yaml:"security"`
}

type Admin struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"` // plain text bootstrap password (only used at first start)
}

type Security struct {
	AllowedCIDRs        []string `yaml:"allowed_cidrs"`         // extra CIDRs allowed beyond RFC1918/loopback
	AllowAnyDestination bool     `yaml:"allow_any_destination"` // dangerous; disables CIDR check
	BlockedPorts        []int    `yaml:"blocked_ports"`         // additional blocked ports
	MaxRequestBodyMB    int      `yaml:"max_request_body_mb"`   // default 100
	UpstreamTimeoutSec  int      `yaml:"upstream_timeout_sec"`  // default 60
}

// Default returns a config with sensible defaults.
func Default() *Config {
	return &Config{
		Listen:       "0.0.0.0:8080",
		DatabasePath: "./data/portly.db",
		Admin:        Admin{Username: "admin"},
		Security: Security{
			BlockedPorts:       []int{22, 23, 25, 3306, 5432, 6379, 11211, 27017},
			MaxRequestBodyMB:   100,
			UpstreamTimeoutSec: 60,
		},
	}
}

// Load reads YAML from disk if path != "" and exists, then overlays defaults.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "0.0.0.0:8080"
	}
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = "./data/portly.db"
	}
	if cfg.Security.MaxRequestBodyMB == 0 {
		cfg.Security.MaxRequestBodyMB = 100
	}
	if cfg.Security.UpstreamTimeoutSec == 0 {
		cfg.Security.UpstreamTimeoutSec = 60
	}
	if len(cfg.Security.BlockedPorts) == 0 {
		cfg.Security.BlockedPorts = []int{22, 23, 25, 3306, 5432, 6379, 11211, 27017}
	}
	return cfg, nil
}

// AllowedNetworks returns the parsed list of allowed CIDRs (private + extras).
func (c *Config) AllowedNetworks() []*net.IPNet {
	def := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16", // link-local sometimes used by virt nets
	}
	all := append(def, c.Security.AllowedCIDRs...)
	out := make([]*net.IPNet, 0, len(all))
	for _, c := range all {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		out = append(out, ipnet)
	}
	return out
}
