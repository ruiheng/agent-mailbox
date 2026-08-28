package codexhook

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ruiheng/waypost/internal/launchpath"
)

func TestWriteOutputEmitsSessionStartAdditionalContext(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteOutput(&output); err != nil {
		t.Fatalf("WriteOutput() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal(output) error = %v", err)
	}
	specific, ok := payload["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput = %#v, want object", payload["hookSpecificOutput"])
	}
	if got := specific["hookEventName"]; got != "SessionStart" {
		t.Fatalf("hookEventName = %v, want SessionStart", got)
	}
	context, _ := specific["additionalContext"].(string)
	if !strings.Contains(context, "not a new Waypost notice") {
		t.Fatalf("additionalContext = %q, want Waypost notice guard", context)
	}
}

func TestRunUserPromptNudgeUsesActiveSessionWithoutCodexProbe(t *testing.T) {
	t.Setenv("PATH", "")

	input := strings.NewReader(`{
  "hook_event_name": "UserPromptSubmit",
  "prompt": "NOTICE: There might be new delivery in waypost."
}`)
	var output bytes.Buffer
	err := run(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("run(UserPromptSubmit) error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal(output) error = %v", err)
	}
	specific := payload["hookSpecificOutput"].(map[string]any)
	if got := specific["hookEventName"]; got != "UserPromptSubmit" {
		t.Fatalf("hookEventName = %v, want UserPromptSubmit", got)
	}
	additionalContext, _ := specific["additionalContext"].(string)
	if !strings.Contains(additionalContext, "waypost_recv") || !strings.Contains(additionalContext, "this Codex session") || !strings.Contains(additionalContext, "Do not start another Codex process") {
		t.Fatalf("additionalContext = %q, want active-session waypost_recv instruction", additionalContext)
	}
}

func TestRunUserPromptSkipsOrdinaryWaypostDiscussion(t *testing.T) {
	t.Parallel()

	input := strings.NewReader(`{
  "hook_event_name": "UserPromptSubmit",
  "prompt": "Please explain how the Waypost nudge message works."
	}`)
	var output bytes.Buffer
	err := run(context.Background(), input, &output)
	if err != nil {
		t.Fatalf("run(UserPromptSubmit) error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q, want empty", output.String())
	}
}

func TestRunPreToolUseWarnsBeforeWaypostWait(t *testing.T) {
	t.Parallel()

	input := strings.NewReader(`{
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": {
    "command": "waypost --state-dir /tmp/waypost-state wait --for workflow/reviewer --timeout 30s"
  }
}`)
	var output bytes.Buffer
	if err := run(context.Background(), input, &output); err != nil {
		t.Fatalf("run(PreToolUse) error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal(output) error = %v", err)
	}
	specific := payload["hookSpecificOutput"].(map[string]any)
	if got := specific["hookEventName"]; got != "PreToolUse" {
		t.Fatalf("hookEventName = %v, want PreToolUse", got)
	}
	additionalContext, _ := specific["additionalContext"].(string)
	if !strings.Contains(additionalContext, "Do not poll Waypost") || !strings.Contains(additionalContext, "continue other available work") || !strings.Contains(additionalContext, "stop completely") {
		t.Fatalf("additionalContext = %q, want wait polling warning", additionalContext)
	}
}

func TestRunPreToolUseSkipsUnrelatedToolCalls(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"waypost recv --for workflow/reviewer"}}`,
		`{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"command":"waypost wait --for workflow/reviewer"}}`,
	} {
		var output bytes.Buffer
		if err := run(context.Background(), strings.NewReader(input), &output); err != nil {
			t.Fatalf("run(PreToolUse) error = %v", err)
		}
		if output.Len() != 0 {
			t.Fatalf("output = %q, want empty", output.String())
		}
	}
}

func TestLooksLikeWaypostNudge(t *testing.T) {
	t.Parallel()

	for _, prompt := range []string{
		"NOTICE: There might be new delivery in waypost.",
		"notice: there might be new delivery in WAYPOST.",
		"  NOTICE: There might be new delivery in waypost.\n",
	} {
		if !LooksLikeWaypostNudge(prompt) {
			t.Errorf("LooksLikeWaypostNudge(%q) = false, want true", prompt)
		}
	}
	for _, prompt := range []string{
		"Please explain the Waypost nudge message.",
		"NOTICE: deployment completed",
		"NOTICE: Waypost is configured",
		"Waypost delivery is pending",
		"NOTICE: investigate the Waypost message delivery bug",
		"NOTICE: investigate a pending Waypost message delivery bug",
		"NUDGE: check Waypost mail",
	} {
		if LooksLikeWaypostNudge(prompt) {
			t.Errorf("LooksLikeWaypostNudge(%q) = true, want false", prompt)
		}
	}
}

func TestLooksLikeWaypostWaitCommand(t *testing.T) {
	t.Parallel()

	for _, command := range []string{
		"waypost wait --for workflow/reviewer",
		"/home/alice/.local/bin/waypost --state-dir /tmp/waypost wait --timeout 30s",
		`'/opt/Waypost' --state-dir '/tmp/state with spaces' wait --json`,
		`& "C:\Users\alice\.local\bin\waypost.exe" wait --for workflow/reviewer`,
		`waypost.exe --state-dir "C:\Users\alice\Waypost State" wait --timeout 30s`,
		`waypost --state-dir=/tmp/waypost wait`,
	} {
		if !LooksLikeWaypostWaitCommand(command) {
			t.Errorf("LooksLikeWaypostWaitCommand(%q) = false, want true", command)
		}
	}
	for _, command := range []string{
		"echo 'waypost wait --for workflow/reviewer'",
		"waypost recv --for workflow/reviewer",
		"waypost doc wait",
		"my-waypost wait",
		"waypost --state-dir wait",
		"waypost\nwait --for workflow/reviewer",
		"cd /tmp && waypost wait --for workflow/reviewer",
	} {
		if LooksLikeWaypostWaitCommand(command) {
			t.Errorf("LooksLikeWaypostWaitCommand(%q) = true, want false", command)
		}
	}
}

func TestInstallRefreshesManagedGroupsInPlace(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	command := "'/opt/waypost' codex-hook"
	original := `{
  "hooks": {
    "SessionStart": [
      {
        "description": "Waypost Codex compact-context guard",
        "matcher": "^compact$",
        "hooks": [{"type": "command", "command": "'/old/waypost' codex-hook", "statusMessage": "Restoring Waypost compact context"}]
      },
      {
        "description": "trusted compact sibling",
        "matcher": "^resume$",
        "hooks": [{"type": "command", "command": "keep-compact-position"}]
      }
    ],
    "UserPromptSubmit": [
      {
        "description": "Waypost Codex nudge MCP hint",
        "hooks": [{"type": "command", "command": "'/old/waypost' codex-hook", "statusMessage": "Checking Waypost MCP availability"}]
      },
      {
        "description": "trusted prompt sibling",
        "hooks": [{"type": "command", "command": "keep-prompt-position"}]
      }
    ],
    "PreToolUse": [
      {
        "description": "Waypost Codex wait polling guard",
        "matcher": "^Bash$",
        "hooks": [{"type": "command", "command": "'/old/waypost' codex-hook", "statusMessage": "Checking Waypost wait usage"}]
      },
      {
        "description": "trusted tool sibling",
        "matcher": "^Bash$",
        "hooks": [{"type": "command", "command": "keep-tool-position"}]
      }
    ]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}

	result, err := Install(home, command)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if !result.Changed {
		t.Fatal("Install() changed = false, want stale managed commands refreshed")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(hooks.json) error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("Unmarshal(hooks.json) error = %v", err)
	}
	hooks := document["hooks"].(map[string]any)

	compactGroups := hooks["SessionStart"].([]any)
	if !groupHasCommand(compactGroups[0].(map[string]any), command) {
		t.Fatalf("SessionStart[0] = %#v, want refreshed Waypost group", compactGroups[0])
	}
	if !groupHasCommand(compactGroups[1].(map[string]any), "keep-compact-position") {
		t.Fatalf("SessionStart[1] = %#v, want unrelated group at original position", compactGroups[1])
	}

	promptGroups := hooks["UserPromptSubmit"].([]any)
	if !groupHasCommand(promptGroups[0].(map[string]any), command) {
		t.Fatalf("UserPromptSubmit[0] = %#v, want refreshed Waypost group", promptGroups[0])
	}
	if !groupHasCommand(promptGroups[1].(map[string]any), "keep-prompt-position") {
		t.Fatalf("UserPromptSubmit[1] = %#v, want unrelated group at original position", promptGroups[1])
	}

	waitGroups := hooks["PreToolUse"].([]any)
	if !groupHasCommand(waitGroups[0].(map[string]any), command) {
		t.Fatalf("PreToolUse[0] = %#v, want refreshed Waypost group", waitGroups[0])
	}
	if !groupHasCommand(waitGroups[1].(map[string]any), "keep-tool-position") {
		t.Fatalf("PreToolUse[1] = %#v, want unrelated group at original position", waitGroups[1])
	}
}

func TestParseWaypostMCPAvailable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		payload   string
		available bool
		wantError bool
	}{
		{name: "enabled", payload: `[{"name":"waypost","enabled":true}]`, available: true},
		{name: "disabled", payload: `[{"name":"waypost","enabled":false}]`},
		{name: "missing", payload: `[{"name":"other","enabled":true}]`},
		{name: "invalid", payload: `{`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			available, err := parseWaypostMCPAvailable([]byte(tc.payload))
			if (err != nil) != tc.wantError {
				t.Fatalf("parseWaypostMCPAvailable() error = %v, wantError %v", err, tc.wantError)
			}
			if available != tc.available {
				t.Fatalf("parseWaypostMCPAvailable() = %v, want %v", available, tc.available)
			}
		})
	}
}

func TestInstallPreservesUnrelatedHooksAndIsIdempotent(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	original := `{
  "custom": {"keep": true},
  "hooks": {
    "SessionStart": [
      {
        "description": "existing startup hook",
        "matcher": "^startup$",
        "hooks": [{"type": "command", "command": "load-startup"}]
      }
    ],
    "UserPromptSubmit": [
      {
        "description": "existing prompt hook",
        "hooks": [{"type": "command", "command": "inspect-prompt"}]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "^Bash$",
        "hooks": [{"type": "command", "command": "check-bash"}]
      }
    ]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	command := "'/opt/waypost' codex-hook"

	first, err := Install(home, command)
	if err != nil {
		t.Fatalf("Install(first) error = %v", err)
	}
	if !first.Changed || first.Path != path {
		t.Fatalf("Install(first) = %+v, want changed path %q", first, path)
	}
	firstContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(first install) error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(firstContents, &document); err != nil {
		t.Fatalf("Unmarshal(first install) error = %v", err)
	}
	custom := document["custom"].(map[string]any)
	if custom["keep"] != true {
		t.Fatalf("custom field = %#v, want preserved", custom)
	}
	hooks := document["hooks"].(map[string]any)
	preToolGroups := hooks["PreToolUse"].([]any)
	if got := len(preToolGroups); got != 2 {
		t.Fatalf("PreToolUse hooks = %d, want existing plus Waypost", got)
	}
	if !groupHasCommand(preToolGroups[0].(map[string]any), "check-bash") || !groupHasCommand(preToolGroups[1].(map[string]any), command) {
		t.Fatalf("PreToolUse hooks = %#v, want preserved existing group followed by Waypost", preToolGroups)
	}
	if got := len(hooks["SessionStart"].([]any)); got != 2 {
		t.Fatalf("SessionStart hooks = %d, want existing plus Waypost", got)
	}
	if got := len(hooks["UserPromptSubmit"].([]any)); got != 2 {
		t.Fatalf("UserPromptSubmit hooks = %d, want existing plus Waypost", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(hooks.json) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("hooks.json mode = %o, want 640", got)
	}

	second, err := Install(home, command)
	if err != nil {
		t.Fatalf("Install(second) error = %v", err)
	}
	if second.Changed {
		t.Fatalf("Install(second) = %+v, want unchanged", second)
	}
	secondContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(second install) error = %v", err)
	}
	if !bytes.Equal(firstContents, secondContents) {
		t.Fatal("idempotent install rewrote hooks.json")
	}

	diagnosis, err := Doctor(home, command)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if diagnosis.Path != path || diagnosis.Command != command {
		t.Fatalf("Doctor() = %+v, want path %q and command %q", diagnosis, path, command)
	}
}

func TestInstallPreservesSiblingHandlersWhenAdoptingGroups(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	command := "'/opt/waypost' codex-hook"
	original := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "^compact$",
        "custom": "keep-compact",
        "hooks": [
          {"type": "command", "command": "'/opt/waypost' codex-hook"},
          {"type": "command", "command": "keep-compact-sibling"}
        ]
      }
    ],
    "UserPromptSubmit": [
      {
        "description": "Waypost Codex nudge MCP hint",
        "custom": "keep-prompt",
        "hooks": [
          {
            "type": "command",
            "command": "C:\\old-version\\waypost.exe codex-hook",
            "statusMessage": "Checking Waypost MCP availability"
          },
          {"type": "command", "command": "keep-prompt-sibling"}
        ]
      }
    ],
    "PreToolUse": [
      {
        "description": "Waypost Codex wait polling guard",
        "matcher": "^Bash$",
        "custom": "keep-wait",
        "hooks": [
          {
            "type": "command",
            "command": "C:\\old-version\\waypost.exe codex-hook",
            "statusMessage": "Checking Waypost wait usage"
          },
          {"type": "command", "command": "keep-wait-sibling"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}

	if _, err := Install(home, command); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(hooks.json) error = %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("Unmarshal(hooks.json) error = %v", err)
	}
	hooks := document["hooks"].(map[string]any)

	compactGroups := hooks["SessionStart"].([]any)
	if got := len(compactGroups); got != 1 {
		t.Fatalf("SessionStart groups = %d, want mixed group updated in place", got)
	}
	preservedCompact := compactGroups[0].(map[string]any)
	if preservedCompact["custom"] != "keep-compact" || !groupHasCommand(preservedCompact, "keep-compact-sibling") {
		t.Fatalf("preserved compact group = %#v, want custom field and sibling handler", preservedCompact)
	}
	compactHandlers := preservedCompact["hooks"].([]any)
	if !handlerHasCommand(compactHandlers[0], command) || !handlerHasCommand(compactHandlers[1], "keep-compact-sibling") {
		t.Fatalf("compact handlers = %#v, want Waypost and sibling handler positions preserved", compactHandlers)
	}

	promptGroups := hooks["UserPromptSubmit"].([]any)
	if got := len(promptGroups); got != 1 {
		t.Fatalf("UserPromptSubmit groups = %d, want mixed group updated in place", got)
	}
	preservedPrompt := promptGroups[0].(map[string]any)
	if preservedPrompt["custom"] != "keep-prompt" || !groupHasCommand(preservedPrompt, "keep-prompt-sibling") {
		t.Fatalf("preserved prompt group = %#v, want custom field and sibling handler", preservedPrompt)
	}
	promptHandlers := preservedPrompt["hooks"].([]any)
	if !handlerHasCommand(promptHandlers[0], command) || !handlerHasCommand(promptHandlers[1], "keep-prompt-sibling") {
		t.Fatalf("prompt handlers = %#v, want Waypost and sibling handler positions preserved", promptHandlers)
	}

	waitGroups := hooks["PreToolUse"].([]any)
	if got := len(waitGroups); got != 1 {
		t.Fatalf("PreToolUse groups = %d, want mixed group updated in place", got)
	}
	preservedWait := waitGroups[0].(map[string]any)
	if preservedWait["custom"] != "keep-wait" || !groupHasCommand(preservedWait, "keep-wait-sibling") {
		t.Fatalf("preserved wait group = %#v, want custom field and sibling handler", preservedWait)
	}
	waitHandlers := preservedWait["hooks"].([]any)
	if !handlerHasCommand(waitHandlers[0], command) || !handlerHasCommand(waitHandlers[1], "keep-wait-sibling") {
		t.Fatalf("wait handlers = %#v, want Waypost and sibling handler positions preserved", waitHandlers)
	}

	second, err := Install(home, command)
	if err != nil {
		t.Fatalf("Install(second) error = %v", err)
	}
	if second.Changed {
		t.Fatalf("Install(second) = %+v, want migrated mixed groups to be idempotent", second)
	}
	secondContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(second hooks.json) error = %v", err)
	}
	if !bytes.Equal(contents, secondContents) {
		t.Fatal("second install rewrote migrated mixed groups")
	}
}

func TestCurrentCommandUsesStableLauncherPath(t *testing.T) {
	stable := filepath.Join(t.TempDir(), "waypost.exe")
	t.Setenv(launchpath.StableExecutableEnv, stable)

	command, err := CurrentCommand()
	if err != nil {
		t.Fatalf("CurrentCommand() error = %v", err)
	}
	want := quoteCommandPath(stable) + " codex-hook"
	if command != want {
		t.Fatalf("CurrentCommand() = %q, want %q", command, want)
	}
}

func TestCurrentCommandRejectsRelativeLauncherPath(t *testing.T) {
	t.Setenv(launchpath.StableExecutableEnv, "relative/waypost.exe")
	if _, err := CurrentCommand(); err == nil {
		t.Fatal("CurrentCommand() error = nil, want relative stable launcher rejection")
	}
}

func TestManagedGroupsUseShortTimeout(t *testing.T) {
	t.Parallel()

	for name, group := range map[string]map[string]any{
		"compact": compactManagedGroup("waypost codex-hook"),
		"prompt":  promptManagedGroup("waypost codex-hook"),
		"wait":    waitManagedGroup("waypost codex-hook"),
	} {
		handlers := group["hooks"].([]any)
		handler := handlers[0].(map[string]any)
		if got := handler["timeout"]; got != hookTimeoutJSON {
			t.Errorf("%s timeout = %#v, want %d", name, got, hookTimeoutSeconds)
		}
	}
}

func TestInstallRejectsMalformedMatcherGroupsWithoutChangingFile(t *testing.T) {
	t.Parallel()

	for _, event := range []string{"SessionStart", "PreToolUse"} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			path := filepath.Join(home, "hooks.json")
			original := []byte(`{"hooks":{"` + event + `":["malformed"]}}`)
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatalf("WriteFile(hooks.json) error = %v", err)
			}
			if _, err := Install(home, "waypost codex-hook"); err == nil {
				t.Fatal("Install() error = nil, want malformed matcher group rejection")
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(hooks.json) error = %v", err)
			}
			if !bytes.Equal(contents, original) {
				t.Fatalf("hooks.json = %q, want unchanged %q", contents, original)
			}
		})
	}
}

func TestInstallPreservesSymlinkAndUpdatesTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	home := filepath.Join(root, "codex")
	managedDir := filepath.Join(root, "managed")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll(codex home) error = %v", err)
	}
	if err := os.MkdirAll(managedDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(managed dir) error = %v", err)
	}
	target := filepath.Join(managedDir, "hooks.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0o640); err != nil {
		t.Fatalf("WriteFile(target hooks.json) error = %v", err)
	}
	path := filepath.Join(home, "hooks.json")
	if err := os.Symlink(filepath.Join("..", "managed", "hooks.json"), path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable on Windows: %v", err)
		}
		t.Fatalf("Symlink(hooks.json) error = %v", err)
	}

	command := "waypost codex-hook"
	if _, err := Install(home, command); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(hooks.json) error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("hooks.json mode = %v, want symlink preserved", info.Mode())
	}
	targetContents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target hooks.json) error = %v", err)
	}
	if !bytes.Contains(targetContents, []byte(command)) {
		t.Fatalf("target hooks.json = %q, want installed command", targetContents)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat(target hooks.json) error = %v", err)
	}
	if got := targetInfo.Mode().Perm(); got != 0o640 {
		t.Fatalf("target hooks.json mode = %o, want 640", got)
	}
}

func TestInstallPreservesExactJSONNumbers(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	const preciseNumber = "9007199254740993"
	original := []byte(`{"external":{"id":` + preciseNumber + `}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	if _, err := Install(home, "waypost codex-hook"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(hooks.json) error = %v", err)
	}
	if !bytes.Contains(contents, []byte(preciseNumber)) {
		t.Fatalf("hooks.json = %q, want exact number %s", contents, preciseNumber)
	}
}

func TestInstallRejectsInvalidJSONWithoutChangingFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	original := []byte("{not-json\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	if _, err := Install(home, "waypost codex-hook"); err == nil {
		t.Fatal("Install() error = nil, want invalid JSON error")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(hooks.json) error = %v", err)
	}
	if !bytes.Equal(contents, original) {
		t.Fatalf("hooks.json = %q, want original invalid contents %q", contents, original)
	}
}

func TestDoctorRejectsMatcherThatAlsoRunsOnStartup(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	contents := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": ".*",
        "hooks": [{"type": "command", "command": "waypost codex-hook"}]
      }
    ]
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	if _, err := Doctor(home, "waypost codex-hook"); err == nil {
		t.Fatal("Doctor() error = nil, want broad matcher rejection")
	}
}

func TestDoctorRequiresWaypostWaitPollingGuard(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	contents := `{
  "hooks": {
    "SessionStart": [{
      "matcher": "^compact$",
      "hooks": [{"type": "command", "command": "waypost codex-hook", "timeout": 5}]
    }],
    "UserPromptSubmit": [{
      "hooks": [{"type": "command", "command": "waypost codex-hook", "timeout": 5}]
    }]
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	_, err := Doctor(home, "waypost codex-hook")
	if err == nil || !strings.Contains(err.Error(), "wait polling guard") {
		t.Fatalf("Doctor() error = %v, want missing wait polling guard error", err)
	}
}

func TestDoctorRejectsMalformedHookSource(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	contents := `{
  "hooks": {
    "SessionStart": [{
      "matcher": "^compact$",
      "hooks": [{"type": "command", "command": "waypost codex-hook", "timeout": 5}]
    }],
    "UserPromptSubmit": [{
      "hooks": [{"type": "command", "command": "waypost codex-hook", "timeout": 5}]
    }],
    "PreToolUse": ["malformed"]
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	_, err := Doctor(home, "waypost codex-hook")
	if err == nil || !strings.Contains(err.Error(), "PreToolUse") {
		t.Fatalf("Doctor() error = %v, want malformed PreToolUse rejection", err)
	}
}

func TestDoctorRejectsManagedHandlersWithoutShortTimeout(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	path := filepath.Join(home, "hooks.json")
	contents := `{
  "hooks": {
    "SessionStart": [{
      "matcher": "^compact$",
      "hooks": [{"type": "command", "command": "waypost codex-hook"}]
    }],
    "UserPromptSubmit": [{
      "hooks": [{"type": "command", "command": "waypost codex-hook"}]
    }]
  }
}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(hooks.json) error = %v", err)
	}
	if _, err := Doctor(home, "waypost codex-hook"); err == nil {
		t.Fatal("Doctor() error = nil, want missing timeout rejection")
	}
}

func handlerHasCommand(value any, command string) bool {
	handler, ok := value.(map[string]any)
	if !ok {
		return false
	}
	handlerType, _ := handler["type"].(string)
	handlerCommand, _ := handler["command"].(string)
	return handlerType == "command" && handlerCommand == command
}
