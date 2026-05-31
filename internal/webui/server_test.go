package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

func TestGroupsEndpointListsGroups(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	runtime, err := mailbox.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	if _, err := runtime.Store().CreateGroup(context.Background(), "group/web"); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	runtime.Close()

	server := httptest.NewServer(NewServer(stateDir, "group/web"))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/groups")
	if err != nil {
		t.Fatalf("GET /api/groups error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var payload struct {
		DefaultGroup string                `json:"default_group"`
		Groups       []mailbox.GroupRecord `json:"groups"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode(groups) error = %v", err)
	}
	if payload.DefaultGroup != "group/web" {
		t.Fatalf("default_group = %q, want group/web", payload.DefaultGroup)
	}
	if len(payload.Groups) != 1 || payload.Groups[0].Address != "group/web" {
		t.Fatalf("groups = %+v, want group/web", payload.Groups)
	}
}

func TestTranscriptEndpointReturnsBodies(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	runtime, err := mailbox.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	store := runtime.Store()
	group, err := store.CreateGroup(context.Background(), "group/web-transcript")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if _, err := store.Send(context.Background(), mailbox.SendParams{
		ToAddress:     group.Address,
		FromAddress:   "agent/sender",
		Subject:       "subject",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("web body"),
		Group:         true,
	}); err != nil {
		t.Fatalf("Send(group) error = %v", err)
	}
	runtime.Close()

	server := httptest.NewServer(NewServer(stateDir, ""))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/groups/" + url.PathEscape(group.Address) + "/transcript")
	if err != nil {
		t.Fatalf("GET transcript error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var payload struct {
		Messages []mailbox.GroupTranscriptMessage `json:"messages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode(transcript) error = %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(payload.Messages))
	}
	if payload.Messages[0].Body != "web body" {
		t.Fatalf("body = %q, want web body", payload.Messages[0].Body)
	}
	if payload.Messages[0].DisplaySender != "agent/sender" {
		t.Fatalf("display_sender = %q, want agent/sender", payload.Messages[0].DisplaySender)
	}
}

func TestEventsEndpointStreamsNewMessages(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "mailbox-state")
	runtime, err := mailbox.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	store := runtime.Store()
	group, err := store.CreateGroup(context.Background(), "group/web-events")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	runtime.Close()

	server := httptest.NewServer(NewServer(stateDir, ""))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/groups/"+url.PathEscape(group.Address)+"/events", nil)
	if err != nil {
		t.Fatalf("NewRequest(events) error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request = request.WithContext(ctx)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET events error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	secondRuntime, err := mailbox.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(second) error = %v", err)
	}
	if _, err := secondRuntime.Store().Send(context.Background(), mailbox.SendParams{
		ToAddress:     group.Address,
		FromAddress:   "agent/sse",
		Subject:       "sse",
		ContentType:   "text/plain",
		SchemaVersion: "v1",
		Body:          []byte("streamed"),
		Group:         true,
	}); err != nil {
		t.Fatalf("Send(group) error = %v", err)
	}
	secondRuntime.Close()

	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-lines:
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"body":"streamed"`) {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for streamed message")
		}
	}
}
