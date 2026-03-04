package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration values for the sticky-proxy.
type Config struct {
	ProxyPort           string
	RedisAddr           string
	JWTSecret           string
	CacheTTL            time.Duration
	RedisPoolSize       int
	EvictionThreshold   int
	EvictionCooldown    time.Duration
	BackendHealthInterval time.Duration
	LogFormat           string
}

// Load reads configuration from environment variables and validates it.
// It returns an error if required values are missing or invalid.
func Load() (*Config, error) {
	cfg := &Config{}

	// PROXY_PORT — default ":8080"
	cfg.ProxyPort = envOrDefault("PROXY_PORT", ":8080")
	if cfg.ProxyPort != "" && cfg.ProxyPort[0] != ':' {
		cfg.ProxyPort = ":" + cfg.ProxyPort
	}

	// REDIS_ADDR — default "localhost:6379"
	cfg.RedisAddr = envOrDefault("REDIS_ADDR", "localhost:6379")

	// JWT_SECRET — required, no default
	cfg.JWTSecret = os.Getenv("JWT_SECRET")
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable is required but not set")
	}

	// CACHE_TTL — default 24h
	cacheTTL, err := parseDuration("CACHE_TTL", 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("invalid CACHE_TTL: %w", err)
	}
	cfg.CacheTTL = cacheTTL

	// REDIS_POOL_SIZE — default 100
	poolSize, err := parseInt("REDIS_POOL_SIZE", 100)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_POOL_SIZE: %w", err)
	}
	cfg.RedisPoolSize = poolSize

	// EVICTION_THRESHOLD — default 3
	threshold, err := parseInt("EVICTION_THRESHOLD", 3)
	if err != nil {
		return nil, fmt.Errorf("invalid EVICTION_THRESHOLD: %w", err)
	}
	cfg.EvictionThreshold = threshold

	// EVICTION_COOLDOWN — default 1m
	cooldown, err := parseDuration("EVICTION_COOLDOWN", time.Minute)
	if err != nil {
		return nil, fmt.Errorf("invalid EVICTION_COOLDOWN: %w", err)
	}
	cfg.EvictionCooldown = cooldown

	// BACKEND_HEALTH_INTERVAL — default 10s
	healthInterval, err := parseDuration("BACKEND_HEALTH_INTERVAL", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid BACKEND_HEALTH_INTERVAL: %w", err)
	}
	cfg.BackendHealthInterval = healthInterval

	// LOG_FORMAT — default "json", options: "json", "text"
	cfg.LogFormat = envOrDefault("LOG_FORMAT", "json")
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		return nil, fmt.Errorf("invalid LOG_FORMAT %q: must be \"json\" or \"text\"", cfg.LogFormat)
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return strconv.Atoi(v)
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	return time.ParseDuration(v)
}
