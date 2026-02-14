package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/repos"
	"github.com/driftdhq/driftd/internal/runner"
	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/driftdhq/driftd/internal/worker"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type fakeRunner struct {
	mu       sync.Mutex
	drifted  map[string]bool
	failures map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, params *runner.RunParams) (*storage.RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.failures[params.StackPath]; ok {
		return &storage.RunResult{RunAt: time.Now(), Error: err.Error()}, nil
	}

	drifted := f.drifted[params.StackPath]
	return &storage.RunResult{
		Drifted: drifted,
		RunAt:   time.Now(),
	}, nil
}

type scanResp struct {
	Stacks     []string   `json:"stacks"`
	Scan       *apiScan   `json:"scan"`
	Scans      []*apiScan `json:"scans"`
	ActiveScan *apiScan   `json:"active_scan"`
	Error      string     `json:"error"`
}

func newTestServer(t *testing.T, r worker.Runner, stacks []string, startWorker bool, versions *testVersions, cancelInflight bool) (*httptest.Server, *queue.Queue, func()) {
	t.Helper()
	_, server, q, cleanup := newTestServerWithConfig(t, r, stacks, startWorker, versions, cancelInflight, nil)
	return server, q, cleanup
}

func newTestServerWithConfig(t *testing.T, r worker.Runner, stacks []string, startWorker bool, versions *testVersions, cancelInflight bool, mutate func(*config.Config)) (*Server, *httptest.Server, *queue.Queue, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	repoDir := createTestRepo(t, stacks, versions)

	cancelInflightFlag := cancelInflight

	cfg := &config.Config{
		DataDir: t.TempDir(),
		Redis: config.RedisConfig{
			Addr: mr.Addr(),
			DB:   0,
		},
		Worker: config.WorkerConfig{
			Concurrency: 1,
			LockTTL:     2 * time.Minute,
			RetryOnce:   false,
			ScanMaxAge:  1 * time.Minute,
			RenewEvery:  10 * time.Second,
		},
		Repos: []config.RepoConfig{
			{
				Name:                       "repo",
				URL:                        repoDir,
				CancelInflightOnNewTrigger: &cancelInflightFlag,
			},
		},
	}

	if mutate != nil {
		mutate(cfg)
	}

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}

	store := storage.New(cfg.DataDir)
	templatesFS := os.DirFS("testdata")
	staticFS := os.DirFS("testdata")

	srv, err := New(cfg, store, q, templatesFS, staticFS)
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	server := httptest.NewServer(srv.Handler())

	var w *worker.Worker
	if startWorker {
		w = worker.New(q, r, 1, cfg, nil)
		w.Start()
	}

	cleanup := func() {
		if w != nil {
			w.Stop()
		}
		server.Close()
		_ = q.Close()
		mr.Close()
	}

	return srv, server, q, cleanup
}

func newTestServerWithRepoStore(t *testing.T, r worker.Runner, stacks []string, startWorker bool, setup func(store *secrets.RepoStore, intStore *secrets.IntegrationStore, repoDir string), mutate func(*config.Config)) (*Server, *httptest.Server, *queue.Queue, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}

	repoDir := createTestRepo(t, stacks, nil)

	cfg := &config.Config{
		DataDir: t.TempDir(),
		Redis: config.RedisConfig{
			Addr: mr.Addr(),
			DB:   0,
		},
		Worker: config.WorkerConfig{
			Concurrency: 1,
			LockTTL:     2 * time.Minute,
			RetryOnce:   false,
			ScanMaxAge:  1 * time.Minute,
			RenewEvery:  10 * time.Second,
		},
		Repos: nil,
	}
	if mutate != nil {
		mutate(cfg)
	}

	q, err := queue.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Worker.LockTTL)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}

	store := storage.New(cfg.DataDir)
	templatesFS := os.DirFS("testdata")
	staticFS := os.DirFS("testdata")

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	encryptor, err := secrets.NewEncryptor(key)
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	repoStore := secrets.NewRepoStore(cfg.DataDir, encryptor)
	intStore := secrets.NewIntegrationStore(cfg.DataDir)
	if setup != nil {
		setup(repoStore, intStore, repoDir)
	}
	repoProvider := repos.NewCombinedProvider(cfg, repoStore, intStore, cfg.DataDir)

	srv, err := New(
		cfg,
		store,
		q,
		templatesFS,
		staticFS,
		WithRepoStore(repoStore),
		WithIntegrationStore(intStore),
		WithRepoProvider(repoProvider),
	)
	if err != nil {
		t.Fatalf("server: %v", err)
	}

	server := httptest.NewServer(srv.Handler())

	var w *worker.Worker
	if startWorker {
		w = worker.New(q, r, 1, cfg, repoProvider)
		w.Start()
	}

	cleanup := func() {
		if w != nil {
			w.Stop()
		}
		server.Close()
		_ = q.Close()
		mr.Close()
	}

	return srv, server, q, cleanup
}

func waitForScan(t *testing.T, ts *httptest.Server, scanID string, timeout time.Duration) *apiScan {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/scans/" + scanID)
		if err != nil {
			t.Fatalf("get scan: %v", err)
		}
		var scan apiScan
		if err := json.NewDecoder(resp.Body).Decode(&scan); err != nil {
			resp.Body.Close()
			t.Fatalf("decode scan: %v", err)
		}
		resp.Body.Close()

		if scan.Status != queue.ScanStatusRunning {
			return &scan
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("scan %s did not complete within timeout", scanID)
	return nil
}

func getScan(t *testing.T, ts *httptest.Server, scanID string) *apiScan {
	t.Helper()

	resp, err := http.Get(ts.URL + "/api/scans/" + scanID)
	if err != nil {
		t.Fatalf("get scan: %v", err)
	}
	defer resp.Body.Close()

	var scan apiScan
	if err := json.NewDecoder(resp.Body).Decode(&scan); err != nil {
		t.Fatalf("decode scan: %v", err)
	}
	return &scan
}

type testVersions struct {
	rootTF  string
	rootTG  string
	stackTF map[string]string
	stackTG map[string]string
}

func createTestRepo(t *testing.T, stacks []string, versions *testVersions) string {
	t.Helper()

	dir := t.TempDir()
	if versions != nil {
		if versions.rootTF != "" {
			if err := os.WriteFile(filepath.Join(dir, ".terraform-version"), []byte(versions.rootTF), 0644); err != nil {
				t.Fatalf("write root tf version: %v", err)
			}
		}
		if versions.rootTG != "" {
			if err := os.WriteFile(filepath.Join(dir, ".terragrunt-version"), []byte(versions.rootTG), 0644); err != nil {
				t.Fatalf("write root tg version: %v", err)
			}
		}
	}
	for _, stack := range stacks {
		path := filepath.Join(dir, stack)
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatalf("mkdir stack: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "main.tf"), []byte(""), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		if versions != nil {
			if v := versions.stackTF[stack]; v != "" {
				if err := os.WriteFile(filepath.Join(path, ".terraform-version"), []byte(v), 0644); err != nil {
					t.Fatalf("write stack tf version: %v", err)
				}
			}
			if v := versions.stackTG[stack]; v != "" {
				if err := os.WriteFile(filepath.Join(path, ".terragrunt-version"), []byte(v), 0644); err != nil {
					t.Fatalf("write stack tg version: %v", err)
				}
			}
		}
	}
	if len(stacks) == 0 {
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("empty repo"), 0644); err != nil {
			t.Fatalf("write placeholder: %v", err)
		}
	}

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("."); err != nil {
		t.Fatalf("git add: %v", err)
	}
	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return dir
}

func computeTestHMAC(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
