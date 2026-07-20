package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ruiheng/waypost/internal/waypost"
)

type blockingReader struct {
	started chan struct{}
}

func (r blockingReader) Read([]byte) (int, error) {
	close(r.started)
	select {}
}

func TestGroupsEndpointListsGroups(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	runtime, err := waypost.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	if _, err := runtime.Store().CreateGroup(context.Background(), "group/web"); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	runtime.Close()

	handler := NewServer(stateDir, "group/web")
	defer handler.Close()
	server := httptest.NewServer(handler)
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
		Groups       []waypost.GroupRecord `json:"groups"`
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

func TestPromptForURLActionOpensURL(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var opened string
	promptForURLAction(context.Background(), Options{
		Stdin:       strings.NewReader("o\n"),
		Stdout:      &stdout,
		Interactive: true,
		OpenURL: func(_ context.Context, webURL string) error {
			opened = webURL
			return nil
		},
	}, "http://127.0.0.1:12345")

	if opened != "http://127.0.0.1:12345" {
		t.Fatalf("opened URL = %q, want URL", opened)
	}
	if !strings.Contains(stdout.String(), "opened http://127.0.0.1:12345") {
		t.Fatalf("stdout = %q, want opened notice", stdout.String())
	}
}

func TestPromptForURLActionCopiesURL(t *testing.T) {
	t.Parallel()

	var copied string
	promptForURLAction(context.Background(), Options{
		Stdin:       strings.NewReader("c\n"),
		Stdout:      &bytes.Buffer{},
		Interactive: true,
		CopyText: func(_ context.Context, text string) error {
			copied = text
			return nil
		},
	}, "http://127.0.0.1:23456")

	if copied != "http://127.0.0.1:23456" {
		t.Fatalf("copied URL = %q, want URL", copied)
	}
}

func TestPromptForURLActionSkipsWhenNonInteractive(t *testing.T) {
	t.Parallel()

	called := false
	promptForURLAction(context.Background(), Options{
		Stdin:       strings.NewReader("o\n"),
		Stdout:      &bytes.Buffer{},
		Interactive: false,
		OpenURL: func(context.Context, string) error {
			called = true
			return nil
		},
	}, "http://127.0.0.1:34567")

	if called {
		t.Fatal("non-interactive prompt called opener")
	}
}

func TestPromptForURLActionReturnsWhenContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	reader := blockingReader{started: make(chan struct{})}
	done := make(chan struct{})

	go func() {
		defer close(done)
		promptForURLAction(ctx, Options{
			Stdin:       reader,
			Stdout:      &bytes.Buffer{},
			Interactive: true,
		}, "http://127.0.0.1:45678")
	}()

	<-reader.started
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("promptForURLAction did not return after context cancellation")
	}
}

func TestRunWarnsForNonLoopbackListenAddress(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer

	err := Run(ctx, Options{Listen: "0.0.0.0:0", Stdout: &stdout})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if !strings.Contains(stdout.String(), "warning: group web read-only UI is listening beyond loopback") {
		t.Fatalf("stdout = %q, want non-loopback warning", stdout.String())
	}
}

func TestRunDoesNotWarnForLoopbackListenAddress(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout bytes.Buffer

	err := Run(ctx, Options{Listen: "127.0.0.1:0", Stdout: &stdout})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if strings.Contains(stdout.String(), "warning:") {
		t.Fatalf("stdout = %q, want no warning", stdout.String())
	}
}

func TestCopyTextWithSystemCommandContinuesAfterCommandFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux clipboard fallback order test")
	}

	binDir := t.TempDir()
	outputPath := filepath.Join(t.TempDir(), "copied.txt")
	if err := os.WriteFile(filepath.Join(binDir, "wl-copy"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write wl-copy stub error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "xclip"), []byte("#!/bin/sh\n/bin/cat > \"$COPY_OUT\"\n"), 0o755); err != nil {
		t.Fatalf("write xclip stub error = %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("COPY_OUT", outputPath)

	if err := copyTextWithSystemCommand(context.Background(), "http://127.0.0.1:12345"); err != nil {
		t.Fatalf("copyTextWithSystemCommand() error = %v", err)
	}
	copied, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read copied output error = %v", err)
	}
	if string(copied) != "http://127.0.0.1:12345" {
		t.Fatalf("copied text = %q, want URL", string(copied))
	}
}

func TestTranscriptEndpointReturnsBodies(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	runtime, err := waypost.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	store := runtime.Store()
	group, err := store.CreateGroup(context.Background(), "group/web-transcript")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if _, err := store.Send(context.Background(), waypost.SendParams{
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

	handler := NewServer(stateDir, "")
	defer handler.Close()
	server := httptest.NewServer(handler)
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
		Messages []waypost.GroupTranscriptMessage `json:"messages"`
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

func TestServerReusesWaypostRuntime(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	runtime, err := waypost.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	if _, err := runtime.Store().CreateGroup(context.Background(), "group/web-reuse"); err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	runtime.Close()

	handler := NewServer(stateDir, "")
	defer handler.Close()
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, path := range []string{
		"/api/groups",
		"/api/groups/" + url.PathEscape("group/web-reuse") + "/transcript",
	} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, response.StatusCode)
		}
	}

	first := handler.runtime
	if first == nil {
		t.Fatal("server did not open waypost runtime")
	}
	response, err := http.Get(server.URL + "/api/groups")
	if err != nil {
		t.Fatalf("GET /api/groups error = %v", err)
	}
	response.Body.Close()
	if handler.runtime != first {
		t.Fatal("server opened a new waypost runtime instead of reusing the existing one")
	}
}

func TestEventsEndpointStreamsNewMessages(t *testing.T) {
	t.Parallel()

	stateDir := filepath.Join(t.TempDir(), "waypost-state")
	runtime, err := waypost.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime() error = %v", err)
	}
	store := runtime.Store()
	group, err := store.CreateGroup(context.Background(), "group/web-events")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	runtime.Close()

	handler := NewServer(stateDir, "")
	defer handler.Close()
	server := httptest.NewServer(handler)
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

	secondRuntime, err := waypost.OpenRuntime(context.Background(), stateDir)
	if err != nil {
		t.Fatalf("OpenRuntime(second) error = %v", err)
	}
	if _, err := secondRuntime.Store().Send(context.Background(), waypost.SendParams{
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
