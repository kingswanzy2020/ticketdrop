// Package config reads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Loader reads environment variables and collects every problem it finds, so a
// misconfigured service reports all of its missing settings in one failed
// start instead of one per restart.
type Loader struct {
	errs []error
}

// String returns the variable's value, or def when it is unset or empty.
func (l *Loader) String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Required returns the variable's value and records an error when it is unset.
func (l *Loader) Required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		l.errs = append(l.errs, fmt.Errorf("%s is required", key))
	}
	return v
}

// Int returns the variable parsed as an integer, or def when it is unset.
func (l *Loader) Int(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

// Float returns the variable parsed as a decimal number, or def when it is unset.
func (l *Loader) Float(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not a number", key, v))
		return def
	}
	return f
}

// Duration returns the variable parsed with time.ParseDuration ("5s", "250ms"),
// or def when it is unset.
func (l *Loader) Duration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not a duration", key, v))
		return def
	}
	return d
}

// Err returns every problem recorded so far, or nil.
func (l *Loader) Err() error {
	return errors.Join(l.errs...)
}
