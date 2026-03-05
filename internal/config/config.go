package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all configuration values for the sticky-proxy.
type Config struct {
	ProxyPort               string
	RedisAddr               string
	JWTSecret               string
	CacheTTL                time.Duration
	RedisPoolSize           int
	RedisMinIdleConns       int
	RedisCBThreshold        int
	RedisCBCooldown         time.Duration
	JWTCacheMaxSize         int
	EvictionThreshold       int
	EvictionCooldown        time.Duration
	BackendHealthInterval   time.Duration
	LogFormat               string
	RoutingClaim            string
	HooksEnabled            bool
	HooksTimeout            time.Duration
	HooksRetries            int
	DrainTimeout            time.Duration
	DrainMaxConcurrent      int
	DrainOnUnhealthy        bool
	RoutingMode             string
	AccountsDiscovery       string
	AccountsQuery           string
	AccountsRefreshInterval time.Duration
	PostgresDSN             string
	RebalanceStrategy       string
	RebalanceOnScale        bool
	RebalanceMaxConcurrent  int
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

	// REDIS_MIN_IDLE_CONNS — default 10
	minIdle, err := parseInt("REDIS_MIN_IDLE_CONNS", 10)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_MIN_IDLE_CONNS: %w", err)
	}
	cfg.RedisMinIdleConns = minIdle

	// REDIS_CB_THRESHOLD — default 5 (failures before circuit opens)
	cbThreshold, err := parseInt("REDIS_CB_THRESHOLD", 5)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_CB_THRESHOLD: %w", err)
	}
	cfg.RedisCBThreshold = cbThreshold

	// REDIS_CB_COOLDOWN — default 30s
	cbCooldown, err := parseDuration("REDIS_CB_COOLDOWN", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_CB_COOLDOWN: %w", err)
	}
	cfg.RedisCBCooldown = cbCooldown

	// JWT_CACHE_MAX_SIZE — default 100000
	jwtCacheMax, err := parseInt("JWT_CACHE_MAX_SIZE", 100000)
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_CACHE_MAX_SIZE: %w", err)
	}
	cfg.JWTCacheMaxSize = jwtCacheMax

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

	// ROUTING_CLAIM — default "sub"
	cfg.RoutingClaim = envOrDefault("ROUTING_CLAIM", "sub")

	// HOOKS_ENABLED — default false
	cfg.HooksEnabled = os.Getenv("HOOKS_ENABLED") == "true"

	// HOOKS_TIMEOUT — default 5s
	hooksTimeout, err := parseDuration("HOOKS_TIMEOUT", 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid HOOKS_TIMEOUT: %w", err)
	}
	cfg.HooksTimeout = hooksTimeout

	// HOOKS_RETRIES — default 2
	hooksRetries, err := parseInt("HOOKS_RETRIES", 2)
	if err != nil {
		return nil, fmt.Errorf("invalid HOOKS_RETRIES: %w", err)
	}
	cfg.HooksRetries = hooksRetries

	// DRAIN_TIMEOUT — default 60s
	drainTimeout, err := parseDuration("DRAIN_TIMEOUT", 60*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid DRAIN_TIMEOUT: %w", err)
	}
	cfg.DrainTimeout = drainTimeout

	// DRAIN_MAX_CONCURRENT — default 10
	drainMaxConcurrent, err := parseInt("DRAIN_MAX_CONCURRENT", 10)
	if err != nil {
		return nil, fmt.Errorf("invalid DRAIN_MAX_CONCURRENT: %w", err)
	}
	cfg.DrainMaxConcurrent = drainMaxConcurrent

	// DRAIN_ON_UNHEALTHY — default false
	cfg.DrainOnUnhealthy = os.Getenv("DRAIN_ON_UNHEALTHY") == "true"

	// ROUTING_MODE — default "hash", options: "hash", "assignment"
	cfg.RoutingMode = envOrDefault("ROUTING_MODE", "hash")
	if cfg.RoutingMode != "hash" && cfg.RoutingMode != "assignment" {
		return nil, fmt.Errorf("invalid ROUTING_MODE %q: must be \"hash\" or \"assignment\"", cfg.RoutingMode)
	}

	// ACCOUNTS_DISCOVERY — default "" (disabled), options: "redis", "http", "postgres"
	cfg.AccountsDiscovery = os.Getenv("ACCOUNTS_DISCOVERY")
	if cfg.AccountsDiscovery != "" && cfg.AccountsDiscovery != "redis" && cfg.AccountsDiscovery != "http" && cfg.AccountsDiscovery != "postgres" {
		return nil, fmt.Errorf("invalid ACCOUNTS_DISCOVERY %q: must be \"\", \"redis\", \"http\", or \"postgres\"", cfg.AccountsDiscovery)
	}

	// ACCOUNTS_QUERY — Redis set key, HTTP URL, or SQL query for account source
	cfg.AccountsQuery = os.Getenv("ACCOUNTS_QUERY")

	// POSTGRES_DSN — required when ACCOUNTS_DISCOVERY=postgres
	cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")

	// ACCOUNTS_REFRESH_INTERVAL — default 30s
	accountsInterval, err := parseDuration("ACCOUNTS_REFRESH_INTERVAL", 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("invalid ACCOUNTS_REFRESH_INTERVAL: %w", err)
	}
	cfg.AccountsRefreshInterval = accountsInterval

	// REBALANCE_STRATEGY — default "none", options: "none", "least-loaded", "consistent-hash"
	cfg.RebalanceStrategy = envOrDefault("REBALANCE_STRATEGY", "none")
	if cfg.RebalanceStrategy != "none" && cfg.RebalanceStrategy != "least-loaded" && cfg.RebalanceStrategy != "consistent-hash" {
		return nil, fmt.Errorf("invalid REBALANCE_STRATEGY %q: must be \"none\", \"least-loaded\", or \"consistent-hash\"", cfg.RebalanceStrategy)
	}

	// REBALANCE_ON_SCALE — default false
	cfg.RebalanceOnScale = os.Getenv("REBALANCE_ON_SCALE") == "true"

	// REBALANCE_MAX_CONCURRENT — default 10
	rebalanceMaxConcurrent, err := parseInt("REBALANCE_MAX_CONCURRENT", 10)
	if err != nil {
		return nil, fmt.Errorf("invalid REBALANCE_MAX_CONCURRENT: %w", err)
	}
	cfg.RebalanceMaxConcurrent = rebalanceMaxConcurrent

	// Cross-field validation
	if cfg.AccountsDiscovery != "" && cfg.RoutingMode != "assignment" {
		return nil, fmt.Errorf("ACCOUNTS_DISCOVERY requires ROUTING_MODE=assignment")
	}
	if cfg.AccountsDiscovery == "postgres" && cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("ACCOUNTS_DISCOVERY=postgres requires POSTGRES_DSN")
	}
	if cfg.RebalanceStrategy != "none" && cfg.RoutingMode != "assignment" {
		return nil, fmt.Errorf("REBALANCE_STRATEGY requires ROUTING_MODE=assignment")
	}

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
