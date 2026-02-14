package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/driftdhq/driftd/internal/storage"
)

func TestParseProjectListParamsDefaults(t *testing.T) {
	req := &http.Request{URL: &url.URL{RawQuery: ""}}
	page, per, sortBy, order := parseProjectListParams(req)
	if page != 1 || per != 50 || sortBy != "path" || order != "asc" {
		t.Fatalf("unexpected defaults: page=%d per=%d sort=%s order=%s", page, per, sortBy, order)
	}
}

func TestParseProjectListParamsClampAndNormalize(t *testing.T) {
	req := &http.Request{URL: &url.URL{RawQuery: "page=-2&per=500&sort=unknown&order=desc"}}
	page, per, sortBy, order := parseProjectListParams(req)
	if page != 1 || per != 200 || sortBy != "path" || order != "desc" {
		t.Fatalf("unexpected normalized params: page=%d per=%d sort=%s order=%s", page, per, sortBy, order)
	}
}

func TestSortStacksByStatus(t *testing.T) {
	now := time.Now()
	stacks := []storage.StackStatus{
		{Path: "b", Drifted: false, Error: ""},
		{Path: "a", Drifted: true, Error: ""},
		{Path: "c", Drifted: false, Error: "boom", RunAt: now},
	}
	sorted := sortStacks(stacks, "status", "asc")
	if sorted[0].Error == "" || sorted[0].Path != "c" {
		t.Fatalf("expected error first, got %v", sorted[0])
	}
	if !sorted[1].Drifted || sorted[1].Path != "a" {
		t.Fatalf("expected drifted second, got %v", sorted[1])
	}
	if sorted[2].Path != "b" {
		t.Fatalf("expected healthy last, got %v", sorted[2])
	}
}

func TestSortStacksByLastRunDesc(t *testing.T) {
	t1 := time.Now().Add(-2 * time.Hour)
	t2 := time.Now().Add(-1 * time.Hour)
	stacks := []storage.StackStatus{
		{Path: "a", RunAt: t1},
		{Path: "b", RunAt: t2},
	}
	sorted := sortStacks(stacks, "last_run", "desc")
	if sorted[0].Path != "b" {
		t.Fatalf("expected most recent first, got %v", sorted[0])
	}
}

func TestPaginateStacks(t *testing.T) {
	stacks := []storage.StackStatus{
		{Path: "a"}, {Path: "b"}, {Path: "c"}, {Path: "d"},
	}
	pageStacks, pagination := paginateStacks(stacks, 2, 2, "/projects/project", "path", "asc")
	if len(pageStacks) != 2 || pageStacks[0].Path != "c" {
		t.Fatalf("unexpected page stacks: %+v", pageStacks)
	}
	if pagination.Page != 2 || pagination.TotalPages != 2 {
		t.Fatalf("unexpected pagination: %+v", pagination)
	}
	if pagination.PrevURL == "" || pagination.NextURL != "" {
		t.Fatalf("unexpected pagination URLs: prev=%q next=%q", pagination.PrevURL, pagination.NextURL)
	}
}
