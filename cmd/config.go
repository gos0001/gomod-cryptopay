package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	Addr string `json:"addr"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{Addr: ":8080"}
	return cfg, f.Section("app", &cfg)
}

func waitForShutdown(fn func()) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fn()
}
