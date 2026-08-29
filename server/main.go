package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	config, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if !strings.HasPrefix(config.DatabasePath, "file:") && config.DatabasePath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(config.DatabasePath), 0o700); err != nil {
			log.Fatalf("创建数据库目录: %v", err)
		}
	}
	store, err := OpenStore(config.DatabasePath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	service := NewService(config, store, NewTailscaleClient(config), log.Default())
	httpServer := &http.Server{
		Addr:              config.ListenAddr,
		Handler:           service.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// The admin SSE endpoints are long-lived; their own heartbeat and
		// request context provide the liveness boundary.
		WriteTimeout:   0,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 32 << 10,
	}

	ctx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignal()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				service.ReapOnce(ctx, time.Now())
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	service.logger.Printf("PinNode server listening on %s", config.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
