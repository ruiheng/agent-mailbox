package codexhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ruiheng/waypost/internal/launchpath"
)

const (
	compactManagedDescription = "Waypost Codex compact-context guard"
	promptManagedDescription  = "Waypost Codex nudge MCP hint"
	compactStatusMessage      = "Restoring Waypost compact context"
	promptStatusMessage       = "Preparing Waypost receive hint"
	legacyPromptStatusMessage = "Checking Waypost MCP availability"
	defaultNudgeMessage       = "NOTICE: There might be new delivery in waypost."
)

const hookTimeoutSeconds int64 = 5
const hookTimeoutJSON json.Number = "5"

const AdditionalContext = `COMPACTION CONTINUATION:
Compaction itself is not a new Waypost notice.
Do not check or receive Waypost merely because the compacted summary mentions historical notices or a future conditional Waypost step.
Only check Waypost after a fresh live NOTICE, an explicit user request, or while continuing an already-claimed delivery.
Resume the task that was active before compaction.`

const MCPNudgeContext = `The current user message appears to be a Waypost nudge.
If the Waypost MCP tool waypost_recv is available in this Codex session, use it to receive the pending delivery.
Otherwise use the normal Waypost CLI receive workflow. Do not start another Codex process to infer which MCP tools this session has.`

type hookInput struct {
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`
	Prompt        string `json:"prompt"`
}

type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type InstallResult struct {
	Path    string
	Changed bool
}

type DoctorResult struct {
	Path    string
	Command string
}

func Run(ctx context.Context, r io.Reader, w io.Writer) error {
	return run(ctx, r, w)
}

func run(_ context.Context, r io.Reader, w io.Writer) error {
	input, hasInput, err := readHookInput(r)
	if err != nil {
		return err
	}
	if !hasInput {
		return writeOutput(w, "SessionStart", AdditionalContext)
	}

	switch input.HookEventName {
	case "SessionStart":
		if input.Source != "compact" {
			return nil
		}
		return writeOutput(w, "SessionStart", AdditionalContext)
	case "UserPromptSubmit":
		if !LooksLikeWaypostNudge(input.Prompt) {
			return nil
		}
		return writeOutput(w, "UserPromptSubmit", MCPNudgeContext)
	default:
		return nil
	}
}

func WriteOutput(w io.Writer) error {
	return writeOutput(w, "SessionStart", AdditionalContext)
}

func writeOutput(w io.Writer, eventName, additionalContext string) error {
	return json.NewEncoder(w).Encode(hookOutput{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     eventName,
			AdditionalContext: additionalContext,
		},
	})
}

func readHookInput(r io.Reader) (hookInput, bool, error) {
	var input hookInput
	err := json.NewDecoder(r).Decode(&input)
	if errors.Is(err, io.EOF) {
		return hookInput{}, false, nil
	}
	if err != nil {
		return hookInput{}, false, fmt.Errorf("parse Codex hook input: %w", err)
	}
	return input, true, nil
}

func LooksLikeWaypostNudge(prompt string) bool {
	return strings.EqualFold(strings.TrimSpace(prompt), defaultNudgeMessage)
}

// CurrentDirectoryWaypostMCPAvailable reports whether Waypost is enabled in
// the effective configuration visible to a new Codex process started in the
// caller's current directory. Trusted project configuration may contribute to
// the result. This is diagnostic only and must not be treated as the effective
// MCP configuration of an already-running Codex session.
func CurrentDirectoryWaypostMCPAvailable(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, "codex", "mcp", "list", "--json").Output()
	if err != nil {
		return false, fmt.Errorf("run `codex mcp list --json`: %w", err)
	}
	return parseWaypostMCPAvailable(output)
}

func parseWaypostMCPAvailable(output []byte) (bool, error) {
	var servers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(output, &servers); err != nil {
		return false, fmt.Errorf("parse `codex mcp list --json`: %w", err)
	}
	for _, server := range servers {
		if server.Name == "waypost" {
			return server.Enabled, nil
		}
	}
	return false, nil
}

func DefaultHome() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		return filepath.Abs(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for Codex hooks: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func CurrentCommand() (string, error) {
	executable, err := launchpath.CurrentExecutable()
	if err != nil {
		return "", fmt.Errorf("resolve waypost executable: %w", err)
	}
	return quoteCommandPath(executable) + " codex-hook", nil
}

func Install(codexHome, command string) (InstallResult, error) {
	path := filepath.Join(codexHome, "hooks.json")
	document, mode, err := readHooksDocument(path)
	if err != nil {
		return InstallResult{}, err
	}

	hooks, err := objectField(document, "hooks")
	if err != nil {
		return InstallResult{}, fmt.Errorf("read %q: %w", path, err)
	}
	if err := validateHooksStructure(hooks); err != nil {
		return InstallResult{}, fmt.Errorf("validate %q: %w", path, err)
	}
	groups, err := arrayField(hooks, "SessionStart")
	if err != nil {
		return InstallResult{}, fmt.Errorf("read %q: %w", path, err)
	}

	updated, compactChanged := mergeManagedHandler(groups, compactManagedGroup(command), managedHandlerSpec{
		description:    compactManagedDescription,
		statusMessages: []string{compactStatusMessage},
		command:        command,
		eligibleGroup: func(group map[string]any) bool {
			return matcherTargetsCompactOnly(group["matcher"])
		},
	})
	hooks["SessionStart"] = updated

	promptGroups, err := arrayField(hooks, "UserPromptSubmit")
	if err != nil {
		return InstallResult{}, fmt.Errorf("read %q: %w", path, err)
	}
	updated, promptChanged := mergeManagedHandler(promptGroups, promptManagedGroup(command), managedHandlerSpec{
		description:    promptManagedDescription,
		statusMessages: []string{promptStatusMessage, legacyPromptStatusMessage},
		command:        command,
		eligibleGroup:  func(map[string]any) bool { return true },
	})
	hooks["UserPromptSubmit"] = updated
	document["hooks"] = hooks
	changed := compactChanged || promptChanged

	if !changed {
		return InstallResult{Path: path, Changed: false}, nil
	}
	if err := writeHooksDocument(path, document, mode); err != nil {
		return InstallResult{}, err
	}
	return InstallResult{Path: path, Changed: true}, nil
}

func Doctor(codexHome, command string) (DoctorResult, error) {
	path := filepath.Join(codexHome, "hooks.json")
	document, _, err := readExistingHooksDocument(path)
	if err != nil {
		return DoctorResult{}, err
	}
	hooks, err := existingObjectField(document, "hooks")
	if err != nil {
		return DoctorResult{}, fmt.Errorf("read %q: %w", path, err)
	}
	if err := validateHooksStructure(hooks); err != nil {
		return DoctorResult{}, fmt.Errorf("validate %q: %w", path, err)
	}
	groups, err := existingArrayField(hooks, "SessionStart")
	if err != nil {
		return DoctorResult{}, fmt.Errorf("read %q: %w", path, err)
	}

	compactInstalled := false
	for _, item := range groups {
		group, ok := item.(map[string]any)
		if !ok || !matcherTargetsCompactOnly(group["matcher"]) {
			continue
		}
		if groupHasCommandWithTimeout(group, command, hookTimeoutSeconds) {
			compactInstalled = true
			break
		}
	}
	if !compactInstalled {
		return DoctorResult{}, fmt.Errorf("Codex compact hook is not installed in %q; run `waypost install codex-hook`", path)
	}

	promptGroups, err := existingArrayField(hooks, "UserPromptSubmit")
	if err != nil {
		return DoctorResult{}, fmt.Errorf("read %q: %w", path, err)
	}
	for _, item := range promptGroups {
		group, ok := item.(map[string]any)
		if ok && groupHasCommandWithTimeout(group, command, hookTimeoutSeconds) {
			return DoctorResult{Path: path, Command: command}, nil
		}
	}
	return DoctorResult{}, fmt.Errorf("Codex Waypost nudge hook is not installed in %q; run `waypost install codex-hook`", path)
}

func compactManagedGroup(command string) map[string]any {
	return map[string]any{
		"description": compactManagedDescription,
		"matcher":     "^compact$",
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       command,
				"statusMessage": compactStatusMessage,
				"timeout":       hookTimeoutJSON,
			},
		},
	}
}

func promptManagedGroup(command string) map[string]any {
	return map[string]any{
		"description": promptManagedDescription,
		"hooks": []any{
			map[string]any{
				"type":          "command",
				"command":       command,
				"statusMessage": promptStatusMessage,
				"timeout":       hookTimeoutJSON,
			},
		},
	}
}

type managedHandlerSpec struct {
	description    string
	statusMessages []string
	command        string
	eligibleGroup  func(map[string]any) bool
}

func mergeManagedHandler(groups []any, desired map[string]any, spec managedHandlerSpec) ([]any, bool) {
	desiredHandlers := desired["hooks"].([]any)
	desiredHandler := desiredHandlers[0]
	updated := make([]any, 0, len(groups)+1)
	installed := false
	for _, item := range groups {
		group, ok := item.(map[string]any)
		if !ok {
			updated = append(updated, item)
			continue
		}

		description, _ := group["description"].(string)
		managedGroup := description == spec.description
		if !managedGroup && !spec.eligibleGroup(group) {
			updated = append(updated, item)
			continue
		}

		handlers, ok := group["hooks"].([]any)
		if !ok {
			updated = append(updated, item)
			continue
		}
		if !installed && managedGroup && len(handlers) == 1 && managedCommandHandler(handlers[0], spec, true, 1) {
			updated = append(updated, desired)
			installed = true
			continue
		}

		kept := make([]any, 0, len(handlers))
		changed := false
		keptManagedHandler := false
		for _, handler := range handlers {
			if !managedCommandHandler(handler, spec, managedGroup, len(handlers)) {
				kept = append(kept, handler)
				continue
			}
			changed = true
			if !installed {
				kept = append(kept, desiredHandler)
				installed = true
				keptManagedHandler = true
			}
		}
		if !changed {
			updated = append(updated, item)
			continue
		}
		if len(kept) != 0 {
			preserved := cloneObject(group)
			preserved["hooks"] = kept
			if managedGroup && !keptManagedHandler {
				delete(preserved, "description")
			}
			updated = append(updated, preserved)
		}
	}
	if !installed {
		updated = append(updated, desired)
	}
	return updated, !reflect.DeepEqual(groups, updated)
}

func managedCommandHandler(value any, spec managedHandlerSpec, managedGroup bool, groupSize int) bool {
	handler, ok := value.(map[string]any)
	if !ok {
		return false
	}
	handlerType, _ := handler["type"].(string)
	if handlerType != "command" {
		return false
	}
	handlerCommand, _ := handler["command"].(string)
	if handlerCommand == spec.command {
		return true
	}
	statusMessage, _ := handler["statusMessage"].(string)
	for _, managedStatus := range spec.statusMessages {
		if statusMessage == managedStatus {
			return true
		}
	}
	return managedGroup && groupSize == 1
}

func cloneObject(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func groupHasCommand(group map[string]any, command string) bool {
	handlers, ok := group["hooks"].([]any)
	if !ok {
		return false
	}
	for _, item := range handlers {
		handler, ok := item.(map[string]any)
		if !ok {
			continue
		}
		handlerType, _ := handler["type"].(string)
		handlerCommand, _ := handler["command"].(string)
		if handlerType == "command" && handlerCommand == command {
			return true
		}
	}
	return false
}

func groupHasCommandWithTimeout(group map[string]any, command string, timeoutSeconds int64) bool {
	handlers, ok := group["hooks"].([]any)
	if !ok {
		return false
	}
	for _, item := range handlers {
		handler, ok := item.(map[string]any)
		if !ok {
			continue
		}
		handlerType, _ := handler["type"].(string)
		handlerCommand, _ := handler["command"].(string)
		timeout, ok := handler["timeout"].(json.Number)
		if handlerType != "command" || handlerCommand != command || !ok {
			continue
		}
		seconds, err := timeout.Int64()
		if err == nil && seconds == timeoutSeconds {
			return true
		}
	}
	return false
}

func matcherTargetsCompactOnly(value any) bool {
	matcher, ok := value.(string)
	if !ok {
		return false
	}
	compiled, err := regexp.Compile(matcher)
	if err != nil || !compiled.MatchString("compact") {
		return false
	}
	for _, other := range []string{"startup", "resume", "clear"} {
		if compiled.MatchString(other) {
			return false
		}
	}
	return true
}

func validateHooksStructure(hooks map[string]any) error {
	for event, value := range hooks {
		groups, ok := value.([]any)
		if !ok {
			if knownHookEvent(event) {
				return fmt.Errorf("hooks.%s must be a JSON array", event)
			}
			continue
		}
		for groupIndex, value := range groups {
			group, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("hooks.%s[%d] must be a JSON object", event, groupIndex)
			}
			if matcher, exists := group["matcher"]; exists {
				if _, ok := matcher.(string); matcher != nil && !ok {
					return fmt.Errorf("hooks.%s[%d].matcher must be a string", event, groupIndex)
				}
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				return fmt.Errorf("hooks.%s[%d].hooks must be a JSON array", event, groupIndex)
			}
			for handlerIndex, handler := range handlers {
				if _, ok := handler.(map[string]any); !ok {
					return fmt.Errorf("hooks.%s[%d].hooks[%d] must be a JSON object", event, groupIndex, handlerIndex)
				}
			}
		}
	}
	return nil
}

func knownHookEvent(name string) bool {
	switch name {
	case "PreToolUse", "PermissionRequest", "PostToolUse", "PreCompact", "PostCompact",
		"SessionStart", "SessionEnd", "UserPromptSubmit", "SubagentStart", "SubagentStop",
		"Stop", "Interrupt":
		return true
	default:
		return false
	}
}

func readHooksDocument(path string) (map[string]any, os.FileMode, error) {
	document, mode, err := readExistingHooksDocument(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, 0o600, nil
	}
	return document, mode, err
}

func readExistingHooksDocument(path string) (map[string]any, os.FileMode, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read Codex hooks %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, 0, fmt.Errorf("parse Codex hooks %q: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, 0, fmt.Errorf("parse Codex hooks %q: %w", path, err)
	}
	if document == nil {
		return nil, 0, fmt.Errorf("parse Codex hooks %q: expected a JSON object", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("stat Codex hooks %q: %w", path, err)
	}
	return document, info.Mode().Perm(), nil
}

func objectField(document map[string]any, name string) (map[string]any, error) {
	value, ok := document[name]
	if !ok {
		return map[string]any{}, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("field %q must be a JSON object", name)
	}
	return object, nil
}

func existingObjectField(document map[string]any, name string) (map[string]any, error) {
	value, ok := document[name]
	if !ok {
		return nil, fmt.Errorf("field %q is missing", name)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("field %q must be a JSON object", name)
	}
	return object, nil
}

func arrayField(document map[string]any, name string) ([]any, error) {
	value, ok := document[name]
	if !ok {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("field %q must be a JSON array", name)
	}
	return array, nil
}

func existingArrayField(document map[string]any, name string) ([]any, error) {
	value, ok := document[name]
	if !ok {
		return nil, fmt.Errorf("field %q is missing", name)
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("field %q must be a JSON array", name)
	}
	return array, nil
}

func writeHooksDocument(path string, document map[string]any, mode os.FileMode) error {
	contents, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Codex hooks %q: %w", path, err)
	}
	contents = append(contents, '\n')
	writePath, err := resolveHooksWritePath(path)
	if err != nil {
		return err
	}
	writeDir := filepath.Dir(writePath)
	if err := os.MkdirAll(writeDir, 0o700); err != nil {
		return fmt.Errorf("create Codex hooks directory %q: %w", writeDir, err)
	}

	temporary, err := os.CreateTemp(writeDir, ".hooks.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary Codex hooks file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set Codex hooks permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary Codex hooks: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary Codex hooks: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Codex hooks: %w", err)
	}
	if err := os.Rename(temporaryPath, writePath); err != nil {
		return fmt.Errorf("replace Codex hooks %q: %w", path, err)
	}
	return nil
}

func resolveHooksWritePath(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Codex hooks %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Codex hooks symlink %q: %w", path, err)
	}
	return resolved, nil
}

func quoteCommandPath(path string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
	}
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}
