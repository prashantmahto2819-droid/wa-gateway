// Vivid Relay: a small self-hosted WhatsApp gateway built on whatsmeow.
// It links one or more WhatsApp numbers (like WhatsApp Web), sends
// messages for your website through a safe queue, and forwards incoming
// messages to your website by webhook. No chat history or media is stored.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	logLevel := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("cannot create data dir", "err", err)
		os.Exit(1)
	}
	db, err := openDB(cfg.DataDir)
	if err != nil {
		log.Error("database error", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hooks := newWebhooks(cfg.WebhookSecret, log)
	hooks.start(ctx, 4)

	waLevel := strings.ToUpper(env("WA_LOG_LEVEL", "WARN"))
	m, err := newManager(ctx, cfg, db, hooks, log, waLevel)
	if err != nil {
		log.Error("startup error", "err", err)
		os.Exit(1)
	}
	if err := m.loadAll(); err != nil {
		log.Error("load sessions", "err", err)
		os.Exit(1)
	}

	api := &API{m: m, cfg: cfg, started: time.Now()}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      3 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		log.Info("Vivid Relay started", "version", version, "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	m.closeAll()
}
