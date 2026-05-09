package main

import (
	"errors"
	"flag"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Host     string
	Listen   string
	Insecure bool
	MaxConns int
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		Host:   os.Getenv("PVE_HOST"),
		Listen: os.Getenv("PVE_LISTEN"),
	}
	cfg.Insecure = envBool("PVE_INSECURE", false)
	cfg.MaxConns = envInt("PVE_MAX_CONNS", 256)

	flag.StringVar(&cfg.Host, "host", cfg.Host, "PVE host, e.g. https://pve.example.com:8006")
	flag.StringVar(&cfg.Listen, "listen", cfg.Listen, "TCP listen address, e.g. 127.0.0.1:5900")
	flag.BoolVar(&cfg.Insecure, "insecure", cfg.Insecure, "skip PVE TLS certificate verification (DANGEROUS)")
	flag.IntVar(&cfg.MaxConns, "max-conns", cfg.MaxConns, "max concurrent client connections")
	flag.Parse()

	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:5900"
	}
	if cfg.Host == "" {
		return nil, errors.New("PVE host required (-host or PVE_HOST)")
	}
	if !strings.HasPrefix(cfg.Host, "http://") && !strings.HasPrefix(cfg.Host, "https://") {
		cfg.Host = "https://" + cfg.Host
	}
	cfg.Host = strings.TrimRight(cfg.Host, "/")

	u, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, err
	}
	if u.Port() == "" {
		if u.Scheme == "https" {
			u.Host = u.Host + ":8006"
		}
		cfg.Host = u.String()
	}

	if cfg.MaxConns < 1 {
		cfg.MaxConns = 1
	}
	return cfg, nil
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}
