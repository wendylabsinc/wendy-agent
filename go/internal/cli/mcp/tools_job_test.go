package mcp

import (
	"context"
	"encoding/json"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"slices"
	"strings"
	"testing"
)

func TestJobInspectRequiresExplicitDirectoryAndConnection(t *testing.T) {
	s := New(&config.Config{}, nil)
	for _, args := range []map[string]any{{}, {"job_path": 1}, {"job_path": "/tmp/job", "since": 2}, {"job_path": "/tmp/job", "wait_seconds": 1.5}} {
		r, err := s.handleJobInspect(context.Background(), callToolReq("job_inspect", args))
		if err != nil || !r.IsError {
			t.Fatalf("accepted invalid arguments: %v %v", r, err)
		}
	}
	r, err := s.handleJobInspect(context.Background(), callToolReq("job_inspect", map[string]any{"job_path": "/tmp/job"}))
	if err != nil || !r.IsError {
		t.Fatalf("unconnected job read: %v %v", r, err)
	}
}
func TestJobInspectDiscoveryAndHostPolicy(t *testing.T) {
	s := New(&config.Config{}, nil)
	srv, err := s.newProtocolServer()
	if err != nil {
		t.Fatal(err)
	}
	tool := srv.GetTool("job_inspect")
	if tool == nil {
		t.Fatal("job inspection not registered")
	}
	if !*tool.Tool.Annotations.ReadOnlyHint || readOnlyTool(tool.Tool) {
		t.Fatal("host read must retain explicit relay host authorization")
	}
	if !slices.Contains(toolGroups["observability"], "job_inspect") {
		t.Fatal("tool adds default schema overhead")
	}
	for _, visible := range protocolTools(t, srv) {
		if visible.Name == "job_inspect" {
			t.Fatal("job tool eagerly loaded in default catalog")
		}
	}
}

func TestTrainingProfileBoundsCatalogWithoutChangingDefaultOrAuthority(t *testing.T) {
	s := New(&config.Config{}, nil)
	if err := s.SetToolProfile("training"); err != nil {
		t.Fatal(err)
	}
	srv, err := s.newProtocolServer()
	if err != nil {
		t.Fatal(err)
	}
	tools := protocolTools(t, srv)
	if len(tools) != 9 {
		t.Fatalf("training catalog contains %d tools", len(tools))
	}
	for _, tool := range tools {
		if !slices.Contains(trainingTools, tool.Name) {
			t.Fatal("irrelevant tool advertised", tool.Name)
		}
	}
	if err = s.SetToolProfile("typo"); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if err = s.SetToolProfile(""); err != nil {
		t.Fatal(err)
	}
	if len(protocolTools(t, srv)) != 16 {
		t.Fatal("profile changed default discovery")
	}
	if readOnlyTool(srv.GetTool("job_inspect").Tool) {
		t.Fatal("catalog profile granted host authority")
	}
}

func TestWorkflowResultHasOneParseableJSONRepresentation(t *testing.T) {
	r := jobJSON(map[string]any{"reported_state": "running", "process": map[string]any{"status": "missing"}, "iteration": 64})
	if r.IsError || len(r.Content) != 1 || r.StructuredContent != nil {
		t.Fatal("duplicate or unusable workflow representation")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 220 || strings.Count(string(encoded), "reported_state") != 1 {
		t.Fatalf("workflow observation duplicated: %s", encoded)
	}
}
