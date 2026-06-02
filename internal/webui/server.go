package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

const (
	defaultListenAddress = "127.0.0.1:0"
	ssePollInterval      = time.Second
)

type Options struct {
	StateDir    string
	Listen      string
	Group       string
	Stdin       io.Reader
	Stdout      io.Writer
	Interactive bool
	OpenURL     func(context.Context, string) error
	CopyText    func(context.Context, string) error
}

type Server struct {
	stateDir string
	group    string
}

func Run(ctx context.Context, opts Options) error {
	listen := strings.TrimSpace(opts.Listen)
	if listen == "" {
		listen = defaultListenAddress
	}

	server := &http.Server{
		Addr:    listen,
		Handler: NewServer(opts.StateDir, opts.Group),
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %q: %w", listen, err)
	}

	webURL := "http://" + listener.Addr().String()
	writeLine(opts.Stdout, "agent-mailbox group web listening on "+webURL)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()

	promptForURLAction(ctx, opts, webURL)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown web server: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func promptForURLAction(ctx context.Context, opts Options, webURL string) {
	if !opts.Interactive || opts.Stdin == nil || opts.Stdout == nil {
		return
	}

	fmt.Fprint(opts.Stdout, "Open URL [o], copy URL [c], or press Enter to continue: ")
	choice, err := bufio.NewReader(opts.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		writeLine(opts.Stdout, "unable to read choice: "+err.Error())
		return
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "o", "open":
		openURL := opts.OpenURL
		if openURL == nil {
			openURL = openURLWithSystemCommand
		}
		if err := openURL(ctx, webURL); err != nil {
			writeLine(opts.Stdout, "unable to open URL: "+err.Error())
			return
		}
		writeLine(opts.Stdout, "opened "+webURL)
	case "c", "copy":
		copyText := opts.CopyText
		if copyText == nil {
			copyText = copyTextWithSystemCommand
		}
		if err := copyText(ctx, webURL); err != nil {
			writeLine(opts.Stdout, "unable to copy URL: "+err.Error())
			return
		}
		writeLine(opts.Stdout, "copied "+webURL)
	default:
	}
}

func openURLWithSystemCommand(ctx context.Context, webURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", webURL).Start()
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", webURL).Start()
	default:
		path, err := exec.LookPath("xdg-open")
		if err != nil {
			return errors.New("xdg-open not found")
		}
		return exec.CommandContext(ctx, path, webURL).Start()
	}
}

func copyTextWithSystemCommand(ctx context.Context, text string) error {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "windows":
		candidates = [][]string{{"clip"}}
	default:
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
	failures := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, path, candidate[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate[0], err))
			continue
		}
		return nil
	}
	if len(failures) > 0 {
		return fmt.Errorf("clipboard commands failed: %s", strings.Join(failures, "; "))
	}
	return errors.New("no clipboard command found")
}

func writeLine(w io.Writer, text string) {
	if w == nil {
		return
	}
	fmt.Fprintln(w, text)
}

func NewServer(stateDir, group string) http.Handler {
	return &Server{
		stateDir: stateDir,
		group:    strings.TrimSpace(group),
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		s.handleIndex(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/groups":
		s.handleGroups(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/groups/"):
		s.handleGroupPath(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, strings.ReplaceAll(indexHTML, "{{DEFAULT_GROUP}}", templateString(s.group)))
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	runtime, err := mailbox.OpenRuntime(r.Context(), s.stateDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer runtime.Close()

	groups, err := runtime.Store().ListGroups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"groups":        groups,
		"default_group": nilIfEmpty(s.group),
	})
}

func (s *Server) handleGroupPath(w http.ResponseWriter, r *http.Request) {
	groupAddress, action, ok := splitGroupAction(r.URL.EscapedPath())
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "transcript":
		s.handleTranscript(w, r, groupAddress)
	case "events":
		s.handleEvents(w, r, groupAddress)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request, groupAddress string) {
	runtime, err := mailbox.OpenRuntime(r.Context(), s.stateDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer runtime.Close()

	messages, err := runtime.Store().ListGroupTranscript(r.Context(), mailbox.GroupTranscriptParams{Address: groupAddress})
	if err != nil {
		writeError(w, statusForMailboxError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"group_address": groupAddress,
		"messages":      messages,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, groupAddress string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	after := strings.TrimSpace(r.URL.Query().Get("after"))
	ticker := time.NewTicker(ssePollInterval)
	defer ticker.Stop()

	for {
		messages, err := s.transcript(r.Context(), groupAddress)
		if err != nil {
			writeSSE(w, "error", map[string]string{"error": err.Error()})
			flusher.Flush()
			return
		}

		for _, message := range messagesAfter(messages, after) {
			writeSSE(w, "message", message)
			after = message.MessageID
			flusher.Flush()
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) transcript(ctx context.Context, groupAddress string) ([]mailbox.GroupTranscriptMessage, error) {
	runtime, err := mailbox.OpenRuntime(ctx, s.stateDir)
	if err != nil {
		return nil, err
	}
	defer runtime.Close()
	return runtime.Store().ListGroupTranscript(ctx, mailbox.GroupTranscriptParams{Address: groupAddress})
}

func splitGroupAction(escapedPath string) (string, string, bool) {
	const prefix = "/api/groups/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(escapedPath, prefix)
	index := strings.LastIndex(rest, "/")
	if index < 0 {
		return "", "", false
	}
	escapedGroup := rest[:index]
	action := rest[index+1:]
	group, err := url.PathUnescape(escapedGroup)
	if err != nil {
		return "", "", false
	}
	return group, action, group != "" && action != ""
}

func messagesAfter(messages []mailbox.GroupTranscriptMessage, after string) []mailbox.GroupTranscriptMessage {
	if after == "" {
		return messages
	}
	for i, message := range messages {
		if message.MessageID == after {
			return messages[i+1:]
		}
	}
	return messages
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeSSE(w io.Writer, event string, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		body = []byte(`{"error":"encode event"}`)
		event = "error"
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
}

func statusForMailboxError(err error) int {
	if errors.Is(err, mailbox.ErrGroupNotFound) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func templateString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}
