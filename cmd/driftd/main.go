package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/driftdhq/driftd/internal/api"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/scheduler"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/driftdhq/driftd/internal/worker"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "worker":
		runWorker(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`driftd - Terraform/Terragrunt drift detection server

Usage:
  driftd <command> [options]

Commands:
  serve    Start the web server (API + UI + scheduler)
  worker   Start a worker process (job processing)

Options:
  -config string   Path to config file (default "config.yaml")

Examples:
  driftd serve -config config.yaml
  driftd worker -config config.yaml`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	// Initialize components
	store := storage.New(cfg.DataDir)

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}
	defer q.Close()

	srv, err := api.New(cfg, store, q, templatesFS, staticFS)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	// Start scheduler
	sched := scheduler.New(q, cfg)
	if err := sched.Start(); err != nil {
		log.Fatalf("failed to start scheduler: %v", err)
	}
	defer sched.Stop()

	// Handle shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("Starting driftd server on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-done
	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func runWorker(args []string) {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("failed to create data dir: %v", err)
	}

	// Initialize components
	store := storage.New(cfg.DataDir)
	run := runner.New(store)

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}
	defer q.Close()

	// Start worker
	w := worker.New(q, run, cfg.Worker.Concurrency, cfg)
	w.Start()

	// Handle shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	<-done
	log.Println("Shutting down, waiting for in-flight jobs...")
	w.Stop()
}
