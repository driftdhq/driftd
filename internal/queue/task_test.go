package queue

import (
	"context"
	"testing"
)

func getTask(t *testing.T, q *Queue, taskID string) *Task {
	t.Helper()

	task, err := q.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return task
}

func TestTaskLifecycle(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()

		task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
		if err != nil {
			t.Fatalf("start task: %v", err)
		}

		job := &Job{
			TaskID:     task.ID,
			RepoName:   "repo",
			RepoURL:    "file:///repo",
			StackPath:  "envs/dev",
			MaxRetries: 0,
		}
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		deq := dequeueJob(t, q)
		if err := q.Complete(ctx, deq, false); err != nil {
			t.Fatalf("complete: %v", err)
		}

		final := getTask(t, q, task.ID)
		if final.Status != TaskStatusCompleted {
			t.Fatalf("expected completed, got %s", final.Status)
		}
	})

	t.Run("failed", func(t *testing.T) {
		q := newTestQueue(t)
		ctx := context.Background()

		task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
		if err != nil {
			t.Fatalf("start task: %v", err)
		}

		job := &Job{
			TaskID:     task.ID,
			RepoName:   "repo",
			RepoURL:    "file:///repo",
			StackPath:  "envs/dev",
			MaxRetries: 0,
		}
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}

		deq := dequeueJob(t, q)
		if err := q.Fail(ctx, deq, "boom"); err != nil {
			t.Fatalf("fail: %v", err)
		}

		final := getTask(t, q, task.ID)
		if final.Status != TaskStatusFailed {
			t.Fatalf("expected failed, got %s", final.Status)
		}
	})
}

func TestMaybeFinishTask(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	jobs := []*Job{
		{TaskID: task.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0},
		{TaskID: task.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/prod", MaxRetries: 0},
	}
	for _, job := range jobs {
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	first := dequeueJob(t, q)
	if err := q.Complete(ctx, first, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	intermediate := getTask(t, q, task.ID)
	if intermediate.Status != TaskStatusRunning {
		t.Fatalf("expected running after first completion, got %s", intermediate.Status)
	}

	second := dequeueJob(t, q)
	if err := q.Fail(ctx, second, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	final := getTask(t, q, task.ID)
	if final.Status != TaskStatusFailed {
		t.Fatalf("expected failed, got %s", final.Status)
	}
}

func TestTaskCounterAccuracy(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 2)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	if got := getTask(t, q, task.ID); got.Queued != 2 || got.Running != 0 {
		t.Fatalf("unexpected initial counts: queued=%d running=%d", got.Queued, got.Running)
	}

	jobs := []*Job{
		{TaskID: task.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0},
		{TaskID: task.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/prod", MaxRetries: 0},
	}
	for _, job := range jobs {
		if err := q.Enqueue(ctx, job); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	first := dequeueJob(t, q)
	state := getTask(t, q, task.ID)
	if state.Running != 1 || state.Queued != 1 {
		t.Fatalf("unexpected counts after dequeue: queued=%d running=%d", state.Queued, state.Running)
	}

	if err := q.Complete(ctx, first, true); err != nil {
		t.Fatalf("complete: %v", err)
	}
	state = getTask(t, q, task.ID)
	if state.Completed != 1 || state.Drifted != 1 || state.Running != 0 || state.Queued != 1 {
		t.Fatalf("unexpected counts after complete: queued=%d running=%d completed=%d drifted=%d", state.Queued, state.Running, state.Completed, state.Drifted)
	}

	second := dequeueJob(t, q)
	state = getTask(t, q, task.ID)
	if state.Running != 1 || state.Queued != 0 {
		t.Fatalf("unexpected counts after second dequeue: queued=%d running=%d", state.Queued, state.Running)
	}

	if err := q.Fail(ctx, second, "boom"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	state = getTask(t, q, task.ID)
	if state.Failed != 1 || state.Errored != 1 || state.Completed != 1 || state.Drifted != 1 {
		t.Fatalf("unexpected final counts: completed=%d failed=%d errored=%d drifted=%d", state.Completed, state.Failed, state.Errored, state.Drifted)
	}
}

func TestCancelTask(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 0)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	if err := q.CancelTask(ctx, task.ID, "repo", "user canceled"); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	canceled := getTask(t, q, task.ID)
	if canceled.Status != TaskStatusCanceled {
		t.Fatalf("expected canceled, got %s", canceled.Status)
	}
	if canceled.Error != "user canceled" {
		t.Fatalf("expected cancel reason, got %q", canceled.Error)
	}

	if _, err := q.GetActiveTask(ctx, "repo"); err != ErrTaskNotFound {
		t.Fatalf("expected no active task, got %v", err)
	}

	locked, err := q.IsRepoLocked(ctx, "repo")
	if err != nil {
		t.Fatalf("is locked: %v", err)
	}
	if locked {
		t.Fatalf("expected repo unlocked")
	}
}

func TestGetLastTask(t *testing.T) {
	q := newTestQueue(t)
	ctx := context.Background()

	task, err := q.StartTask(ctx, "repo", "manual", "", "", 1)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	job := &Job{TaskID: task.ID, RepoName: "repo", RepoURL: "file:///repo", StackPath: "envs/dev", MaxRetries: 0}
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deq := dequeueJob(t, q)
	if err := q.Complete(ctx, deq, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	last, err := q.GetLastTask(ctx, "repo")
	if err != nil {
		t.Fatalf("get last: %v", err)
	}
	if last.ID != task.ID {
		t.Fatalf("expected last task %s, got %s", task.ID, last.ID)
	}
	if last.Status != TaskStatusCompleted {
		t.Fatalf("expected completed, got %s", last.Status)
	}
}
