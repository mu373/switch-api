package main

import (
	"context"
	_ "embed"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const defaultConfigPath = "config.yaml"

//go:embed openapi.yaml
var openAPISpec []byte

func main() {
	configPath := os.Getenv("SWITCH_API_CONFIG")
	if configPath == "" {
		configPath = defaultConfigPath
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	apiKey := os.Getenv("SWITCH_CONTROL_API_KEY")
	if apiKey == "" {
		log.Fatal("SWITCH_CONTROL_API_KEY must be set")
	}
	providerSet, err := newProviders(cfg)
	if err != nil {
		log.Fatalf("configure providers: %v", err)
	}
	service := newSwitchService(cfg, providerSet)
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           newHTTPHandler(service, apiKey),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      cfg.actionDuration + 5*time.Second,
		IdleTimeout:       time.Minute,
	}

	go func() {
		log.Printf("listening on %s with %d configured switch(es); docs at /docs/", cfg.ListenAddr, len(cfg.Switches))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
