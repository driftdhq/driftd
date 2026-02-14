package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/api"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/projects"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/driftdhq/driftd/internal/worker"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func main() {
	port := getenvInt("UI_TEST_PORT", 3939)

	mr, err := miniredis.Run()
	if err != nil {
		log.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	dataDir, err := os.MkdirTemp("", "driftd-ui-data-*")
	if err != nil {
		log.Fatalf("data dir: %v", err)
	}

	projectDir := filepath.Join(dataDir, "project-source")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		log.Fatalf("project dir: %v", err)
	}
	initProjectWithStacks(projectDir, buildTestStacks())

	cfg := &config.Config{
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", port),
		DataDir:    filepath.Join(dataDir, "data"),
		Redis: config.RedisConfig{
			Addr: mr.Addr(),
			DB:   0,
		},
		Worker: config.WorkerConfig{
			Concurrency: 2,
			LockTTL:     30 * time.Second,
			ScanMaxAge:  2 * time.Minute,
			RenewEvery:  5 * time.Second,
		},
		Projects: []config.ProjectConfig{
			{
				Name: "project",
				URL:  "file://" + projectDir,
			},
		},
	}

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		log.Fatalf("queue: %v", err)
	}
	defer q.Close()

	store := storage.New(cfg.DataDir)
	seedStorage(store)

	keyStore := secrets.NewKeyStore(cfg.DataDir)
	encKey, err := keyStore.LoadOrGenerate()
	if err != nil {
		log.Fatalf("encryption: %v", err)
	}
	encryptor, err := secrets.NewEncryptor(encKey)
	if err != nil {
		log.Fatalf("encryptor: %v", err)
	}
	projectStore := secrets.NewProjectStore(cfg.DataDir, encryptor)
	_ = projectStore.Load()
	intStore := secrets.NewIntegrationStore(cfg.DataDir)
	_ = intStore.Load()

	projectProvider := projects.NewCombinedProvider(cfg, projectStore, intStore, cfg.DataDir)

	orch := orchestrate.New(cfg, q)
	defer orch.Stop()

	srv, err := api.New(cfg, store, q, os.DirFS("cmd/driftd"), os.DirFS("cmd/driftd"),
		api.WithProjectStore(projectStore),
		api.WithIntegrationStore(intStore),
		api.WithProjectProvider(projectProvider),
		api.WithOrchestrator(orch),
	)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	defer srv.Stop()

	w := worker.New(q, &uiRunner{}, cfg.Worker.Concurrency, cfg, projectProvider)
	w.Start()
	defer w.Stop()

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("LISTENING http://%s", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

type uiRunner struct{}

// Run implements the worker.Runner interface using a lightweight fake.
func (r *uiRunner) Run(ctx context.Context, params *runner.RunParams) (*storage.RunResult, error) {
	drifted := strings.Contains(params.StackPath, "drift")
	return &storage.RunResult{
		Drifted:   drifted,
		Added:     1,
		Changed:   0,
		Destroyed: 0,
		RunAt:     time.Now(),
	}, nil
}

func buildTestStacks() []string {
	var stacks []string
	for i := 1; i <= 60; i++ {
		stacks = append(stacks, fmt.Sprintf("envs/dev/app-%03d", i))
	}
	for i := 1; i <= 60; i++ {
		stacks = append(stacks, fmt.Sprintf("envs/prod/app-%03d", i))
	}
	stacks = append(stacks, "envs/staging/region/us-east-1/app-001")
	stacks = append(stacks, "envs/staging/region/us-east-1/app-drift")
	return stacks
}

func initProjectWithStacks(dir string, stacks []string) *git.Repository {
	project, err := git.PlainInit(dir, false)
	if err != nil {
		log.Fatalf("init project: %v", err)
	}
	for _, stack := range stacks {
		stackDir := filepath.Join(dir, stack)
		if err := os.MkdirAll(stackDir, 0755); err != nil {
			log.Fatalf("stack dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stackDir, "main.tf"), []byte(`resource "null_resource" "test" {}`), 0644); err != nil {
			log.Fatalf("write stack: %v", err)
		}
	}
	wt, err := project.Worktree()
	if err != nil {
		log.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("."); err != nil {
		log.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "ui-test",
			Email: "ui@test",
			When:  time.Now(),
		},
	}); err != nil {
		log.Fatalf("commit: %v", err)
	}
	return project
}

func seedStorage(store *storage.Storage) {
	now := time.Now()
	for i := 1; i <= 60; i++ {
		path := fmt.Sprintf("envs/dev/app-%03d", i)
		store.SaveResult("project", path, &storage.RunResult{
			Drifted:   i%5 == 0,
			Added:     1,
			Changed:   0,
			Destroyed: 0,
			RunAt:     now.Add(-time.Duration(i) * time.Minute),
		})
	}
	for i := 1; i <= 60; i++ {
		path := fmt.Sprintf("envs/prod/app-%03d", i)
		store.SaveResult("project", path, &storage.RunResult{
			Drifted:   i%7 == 0,
			Added:     0,
			Changed:   1,
			Destroyed: 0,
			RunAt:     now.Add(-time.Duration(i) * time.Minute),
		})
	}
	store.SaveResult("project", "envs/staging/region/us-east-1/app-001", &storage.RunResult{
		Drifted:   false,
		Added:     0,
		Changed:   0,
		Destroyed: 0,
		RunAt:     now.Add(-2 * time.Hour),
	})
	store.SaveResult("project", "envs/staging/region/us-east-1/app-drift", &storage.RunResult{
		Drifted:   true,
		Added:     1,
		Changed:   2,
		Destroyed: 0,
		RunAt:     now.Add(-90 * time.Minute),
	})
}

func getenvInt(key string, fallback int) int {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return parsed
}
