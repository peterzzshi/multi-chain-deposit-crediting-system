package config

import (
	"fmt"
	"os"
	"time"
)

// RequireEnv reads an environment variable and fails if not set.
func RequireEnv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	panic(fmt.Sprintf("required environment variable %s is not set", key))
}

// RequireDuration parses a duration from an environment variable and fails if not set.
func RequireDuration(key string) time.Duration {
	v := RequireEnv(key)
	d, err := time.ParseDuration(v)
	if err != nil {
		panic(fmt.Sprintf("environment variable %s must be a valid duration: %v", key, err))
	}
	return d
}

// RequireUint64 parses a uint64 from an environment variable and fails if not set.
func RequireUint64(key string) uint64 {
	v := RequireEnv(key)
	var n uint64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		panic(fmt.Sprintf("environment variable %s must be a valid uint64: %v", key, err))
	}
	return n
}

func RequirePositiveUint64(key string) uint64 {
	n := RequireUint64(key)
	if n == 0 {
		panic(fmt.Sprintf("environment variable %s must be positive", key))
	}
	return n
}
