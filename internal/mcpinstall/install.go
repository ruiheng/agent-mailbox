package mcpinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ruiheng/waypost/internal/codexhook"
	"github.com/ruiheng/waypost/internal/launchpath"
)

const (
	serverName          = "waypost"
	serverSubcommand    = "mcp"
	configFileName      = "config.toml"
	claudeConfigName    = ".claude.json"
	agyMCPConfigName    = ".gemini/config/mcp_config.json"
	toolTimeoutSeconds  = 660
	commandFailureLimit = 500
)

var requiredEnvVars = []string{
	"TMUX",
	"AGENTDECK_INSTANCE_ID",
	"CODEX_THREAD_ID",
	"CODEX_SESSION_ID",
	"WAYPOST_STATE_DIR",
	"XDG_STATE_HOME",
}

// Result describes the agent configurations changed by an MCP installation.
type Result struct {
	Path    string
	Command string
	Changed bool
}

type commandOutput struct {
	stdout []byte
	stderr []byte
}

type commandRunner func(context.Context, string, ...string) (commandOutput, error)

type dependencies struct {
	run               commandRunner
	resolveExecutable func() (string, error)
	resolveHome       func() (string, error)
	resolveUserHome   func() (string, error)
	lookPath          func(string) (string, error)
}

// Install registers the built-in Waypost MCP server in Codex's global
// configuration and updates detected Claude Code and agy configurations. It
// uses the stable Waypost executable path so upgrades do not leave an agent
// pointing at a versioned binary.
func Install(ctx context.Context) (Result, error) {
	return installWithDependencies(ctx, dependencies{
		run:               runCommand,
		resolveExecutable: launchpath.CurrentExecutable,
		resolveHome:       codexhook.DefaultHome,
		resolveUserHome:   os.UserHomeDir,
		lookPath:          exec.LookPath,
	})
}

func installWithDependencies(ctx context.Context, deps dependencies) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if deps.run == nil || deps.resolveExecutable == nil || deps.resolveHome == nil || deps.lookPath == nil {
		return Result{}, errors.New("MCP installer dependencies are incomplete")
	}

	codexPath, err := deps.lookPath("codex")
	if err != nil {
		return Result{}, fmt.Errorf("find Codex CLI: %w; install Codex before running `waypost install mcp-server`", err)
	}
	var userHome string
	if deps.resolveUserHome != nil {
		userHome, err = deps.resolveUserHome()
		if err != nil {
			return Result{}, fmt.Errorf("resolve user home for optional MCP integrations: %w", err)
		}
		if err := validateOptionalAgents(userHome, deps.lookPath); err != nil {
			return Result{}, err
		}
	}
	home, err := deps.resolveHome()
	if err != nil {
		return Result{}, err
	}
	executable, err := deps.resolveExecutable()
	if err != nil {
		return Result{}, fmt.Errorf("resolve Waypost executable: %w", err)
	}
	configPath := filepath.Join(home, configFileName)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return Result{}, fmt.Errorf("create Codex config directory %q: %w", home, err)
	}

	changed := false
	getOutput, getErr := deps.run(ctx, codexPath, "mcp", "get", serverName, "--json")
	args := []string{serverSubcommand}
	if getErr == nil {
		server, err := parseCodexServer(getOutput.stdout)
		if err != nil {
			return Result{}, fmt.Errorf("inspect Codex MCP server %q: %w", serverName, err)
		}
		if len(server.Transport.Args) > 0 && server.Transport.Args[0] == serverSubcommand {
			args = append([]string(nil), server.Transport.Args...)
		}
	} else {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if !isMissingCodexMCPError(string(getOutput.stderr)) {
			return Result{}, commandError("inspect Codex MCP server", getOutput, getErr)
		}
		if output, err := deps.run(ctx, codexPath, "mcp", "add", serverName, "--", executable, serverSubcommand); err != nil {
			return Result{}, commandError("add Waypost MCP server to Codex", output, err)
		}
		changed = true
	}

	configChanged, err := ensureCodexConfig(configPath, executable, args...)
	if err != nil {
		return Result{}, err
	}
	changed = changed || configChanged
	result := Result{
		Path:    configPath,
		Command: executable + " " + serverSubcommand,
		Changed: changed,
	}

	// The Codex configuration is the required installation target. Other
	// agents are configured when their CLI or existing configuration indicates
	// that they are installed, while leaving unrelated homes untouched.
	if deps.resolveUserHome == nil {
		return result, nil
	}
	optional, err := installOptionalAgents(userHome, executable, deps.lookPath)
	if err != nil {
		return result, fmt.Errorf("Codex MCP server configured at %q; optional integrations failed: %w", configPath, err)
	}
	result.Changed = result.Changed || optional
	return result, nil
}

type codexMCPServer struct {
	Name      string `json:"name"`
	Transport struct {
		Args []string `json:"args"`
	} `json:"transport"`
}

func parseCodexServer(output []byte) (codexMCPServer, error) {
	var server codexMCPServer
	if err := json.Unmarshal(output, &server); err != nil {
		return codexMCPServer{}, err
	}
	if server.Name != serverName {
		return codexMCPServer{}, fmt.Errorf("returned server %q", server.Name)
	}
	return server, nil
}

func ensureCodexConfig(path, executable string, args ...string) (bool, error) {
	contents, err := os.ReadFile(path)
	mode := os.FileMode(0o600)
	if err == nil {
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read Codex config %q: %w", path, err)
	}

	updated, changed, err := ensureWaypostSection(string(contents), executable, args...)
	if err != nil {
		return false, fmt.Errorf("update Codex config %q: %w", path, err)
	}
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create Codex config directory %q: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomically(path, []byte(updated), mode); err != nil {
		return false, fmt.Errorf("write Codex config %q: %w", path, err)
	}
	return true, nil
}

func installOptionalAgents(home, executable string, lookPath func(string) (string, error)) (bool, error) {
	changed := false
	claudePath := claudeConfigPath(home)
	configureClaude, err := shouldConfigureOptionalAgent(claudePath, "claude", lookPath)
	if err != nil {
		return false, fmt.Errorf("inspect Claude Code MCP config %q: %w", claudePath, err)
	}
	if configureClaude {
		claudeChanged, err := ensureClaudeConfig(claudePath, executable)
		if err != nil {
			return false, fmt.Errorf("configure Claude Code MCP server: %w", err)
		}
		changed = changed || claudeChanged
	}

	agyPath := filepath.Join(home, agyMCPConfigName)
	configureAgy, err := shouldConfigureOptionalAgent(agyPath, "agy", lookPath)
	if err != nil {
		return false, fmt.Errorf("inspect agy MCP config %q: %w", agyPath, err)
	}
	if configureAgy {
		agyChanged, err := ensureAgyConfig(agyPath, executable)
		if err != nil {
			return false, fmt.Errorf("configure agy MCP server: %w", err)
		}
		changed = changed || agyChanged
	}
	return changed, nil
}

func claudeConfigPath(home string) string {
	if configured := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); configured != "" {
		return filepath.Join(configured, claudeConfigName)
	}
	return filepath.Join(home, claudeConfigName)
}

func validateOptionalAgents(home string, lookPath func(string) (string, error)) error {
	claudePath := claudeConfigPath(home)
	configureClaude, err := shouldConfigureOptionalAgent(claudePath, "claude", lookPath)
	if err != nil {
		return fmt.Errorf("inspect Claude Code MCP config %q: %w", claudePath, err)
	}
	if configureClaude {
		if err := validateJSONMCPConfig(claudePath); err != nil {
			return fmt.Errorf("validate Claude Code MCP config %q: %w", claudePath, err)
		}
	}

	agyPath := filepath.Join(home, agyMCPConfigName)
	configureAgy, err := shouldConfigureOptionalAgent(agyPath, "agy", lookPath)
	if err != nil {
		return fmt.Errorf("inspect agy MCP config %q: %w", agyPath, err)
	}
	if configureAgy {
		if err := validateJSONMCPConfig(agyPath); err != nil {
			return fmt.Errorf("validate agy MCP config %q: %w", agyPath, err)
		}
	}
	return nil
}

func validateJSONMCPConfig(path string) error {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(contents, &root); err != nil {
		return fmt.Errorf("parse MCP config: %w", err)
	}
	if raw := root["mcpServers"]; len(raw) > 0 {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("parse MCP servers: %w", err)
		}
		if raw := servers[serverName]; len(raw) > 0 {
			var server map[string]json.RawMessage
			if err := json.Unmarshal(raw, &server); err != nil {
				return fmt.Errorf("parse %s MCP server: %w", serverName, err)
			}
		}
	}
	return nil
}

func shouldConfigureOptionalAgent(path, cli string, lookPath func(string) (string, error)) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	_, err := lookPath(cli)
	return err == nil, nil
}

func ensureClaudeConfig(path, executable string) (bool, error) {
	return ensureJSONMCPServer(path, executable, func(server map[string]json.RawMessage) error {
		if err := setJSONField(server, "type", "stdio"); err != nil {
			return err
		}
		if err := setJSONField(server, "command", executable); err != nil {
			return err
		}
		if err := setJSONField(server, "args", []string{serverSubcommand}); err != nil {
			return err
		}
		if err := setJSONField(server, "timeout", toolTimeoutSeconds*1000); err != nil {
			return err
		}
		if _, present := server["env"]; present {
			return nil
		}
		return setJSONField(server, "env", map[string]string{})
	})
}

func ensureAgyConfig(path, executable string) (bool, error) {
	return ensureJSONMCPServer(path, executable, func(server map[string]json.RawMessage) error {
		if err := setJSONField(server, "command", executable); err != nil {
			return err
		}
		if err := setJSONField(server, "args", []string{serverSubcommand}); err != nil {
			return err
		}
		// Remove fields that make agy treat the entry as an HTTP server.
		delete(server, "serverUrl")
		delete(server, "url")
		delete(server, "headers")
		// agy mcp enable removes this field. Removing a persisted true value
		// makes the server active without introducing an undocumented schema key.
		delete(server, "disabled")
		return nil
	})
}

type jsonMCPMutator func(map[string]json.RawMessage) error

func ensureJSONMCPServer(path, executable string, mutate jsonMCPMutator) (bool, error) {
	original, err := os.ReadFile(path)
	mode := os.FileMode(0o600)
	if err == nil {
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read MCP config %q: %w", path, err)
	}

	root := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(original)) > 0 {
		if err := json.Unmarshal(original, &root); err != nil {
			return false, fmt.Errorf("parse MCP config %q: %w", path, err)
		}
		if root == nil {
			root = make(map[string]json.RawMessage)
		}
	}
	mcpServers := make(map[string]json.RawMessage)
	if raw := root["mcpServers"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &mcpServers); err != nil {
			return false, fmt.Errorf("parse MCP servers in %q: %w", path, err)
		}
		if mcpServers == nil {
			mcpServers = make(map[string]json.RawMessage)
		}
	}
	server := make(map[string]json.RawMessage)
	if raw := mcpServers[serverName]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &server); err != nil {
			return false, fmt.Errorf("parse %s MCP server in %q: %w", serverName, path, err)
		}
		if server == nil {
			server = make(map[string]json.RawMessage)
		}
	}
	if err := mutate(server); err != nil {
		return false, fmt.Errorf("build %s MCP server in %q: %w", serverName, path, err)
	}
	serverJSON, err := json.Marshal(server)
	if err != nil {
		return false, fmt.Errorf("encode %s MCP server: %w", serverName, err)
	}
	mcpServers[serverName] = serverJSON
	mcpJSON, err := json.Marshal(mcpServers)
	if err != nil {
		return false, fmt.Errorf("encode MCP servers: %w", err)
	}
	root["mcpServers"] = mcpJSON
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encode MCP config: %w", err)
	}
	updated = append(updated, '\n')
	if bytes.Equal(original, updated) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create MCP config directory %q: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomically(path, updated, mode); err != nil {
		return false, fmt.Errorf("write MCP config %q: %w", path, err)
	}
	return true, nil
}

func writeFileAtomically(path string, contents []byte, mode os.FileMode) error {
	writePath := path
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve config symlink %q: %w", path, err)
		}
		writePath = resolved
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config %q: %w", path, err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(writePath), ".waypost-mcp-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := codexhook.ReplaceFile(temporaryPath, writePath); err != nil {
		return fmt.Errorf("replace config %q: %w", path, err)
	}
	return nil
}

func setJSONField(fields map[string]json.RawMessage, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", key, err)
	}
	fields[key] = encoded
	return nil
}

func ensureWaypostSection(contents, executable string, args ...string) (string, bool, error) {
	const section = "[mcp_servers.waypost]"
	commandLine := "command = " + strconv.Quote(executable)
	preserveExistingArgs := len(args) == 0
	if len(args) == 0 || args[0] != serverSubcommand {
		args = []string{serverSubcommand}
	}
	argsValues := make([]string, len(args))
	for i, arg := range args {
		argsValues[i] = strconv.Quote(arg)
	}
	argsLine := "args = [" + strings.Join(argsValues, ", ") + "]"
	requiredLine := "required = true"
	envLine := "env_vars = [\"" + strings.Join(requiredEnvVars, "\", \"") + "\"]"
	timeoutLine := fmt.Sprintf("tool_timeout_sec = %d", toolTimeoutSeconds)
	required := []struct {
		key  string
		line string
	}{
		{key: "command", line: commandLine},
		{key: "args", line: argsLine},
		{key: "required", line: requiredLine},
		{key: "env_vars", line: envLine},
		{key: "tool_timeout_sec", line: timeoutLine},
	}
	keysToRemove := map[string]bool{
		"url":                  true,
		"bearer_token_env_var": true,
		"http_headers":         true,
		"env_http_headers":     true,
		"http_headers_helper":  true,
		"oauth_resource":       true,
		"oauth":                true,
		// Codex enables MCP servers by default. Removing a persisted false
		// override also re-enables an entry that was previously disabled.
		"enabled": true,
	}

	trimmed := strings.TrimSuffix(contents, "\n")
	lines := []string{}
	if trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	found := false
	inSection := false
	skipSection := false
	inMCPServersTable := false
	atRoot := true
	seen := make(map[string]bool, len(required))
	output := make([]string, 0, len(lines)+len(required)+2)
	appendMissing := func() {
		for _, field := range required {
			if !seen[field.key] {
				output = append(output, field.line)
				seen[field.key] = true
			}
		}
	}

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		lineWithoutCR := strings.TrimSuffix(line, "\r")
		trimmedLine := strings.TrimSpace(lineWithoutCR)
		headerName, headerOK := canonicalTOMLHeader(trimmedLine)
		if skipSection {
			if !headerOK {
				continue
			}
			skipSection = false
		}
		if isWaypostSectionHeader(trimmedLine, section) {
			if inSection {
				appendMissing()
			}
			found = true
			inMCPServersTable = false
			inSection = true
			seen = make(map[string]bool, len(required))
			output = append(output, lineWithoutCR)
			continue
		}
		if inSection && headerOK {
			if isWaypostTransportSubtable(trimmedLine) {
				appendMissing()
				inSection = false
				skipSection = true
				continue
			}
			appendMissing()
			inSection = false
		}
		if headerOK && isWaypostTransportSubtable(trimmedLine) {
			skipSection = true
			continue
		}
		if headerOK {
			atRoot = false
			inMCPServersTable = headerName == "mcp_servers"
		} else if atRoot && isInlineMCPServersAssignment(lineWithoutCR) {
			return "", false, errors.New("inline mcp_servers tables are unsupported; convert it to [mcp_servers.waypost] before installing")
		} else if inMCPServersTable {
			if isInlineWaypostAssignment(lineWithoutCR) {
				return "", false, errors.New("inline mcp_servers.waypost tables are unsupported; convert it to [mcp_servers.waypost] before installing")
			}
		}
		if inSection {
			key := strings.TrimSpace(strings.SplitN(trimmedLine, "=", 2)[0])
			if normalized, ok := normalizeTOMLKeyPart(key); ok {
				key = normalized
			}
			if keysToRemove[key] {
				continue
			}
			if key == "args" || key == "env_vars" {
				end, multiline, ok := tomlArrayEnd(lines, index)
				if !ok {
					return "", false, fmt.Errorf("unterminated %s array in Codex Waypost section", key)
				}
				if key == "env_vars" {
					envLine, err := mergeEnvVarsLine(lines, index, end)
					if err != nil {
						return "", false, fmt.Errorf("parse env_vars in Codex Waypost section: %w", err)
					}
					if seen[key] {
						index = end
						continue
					}
					output = append(output, envLine)
					seen[key] = true
					index = end
					continue
				}
				if multiline {
					if key == "args" && preserveExistingArgs {
						seen[key] = true
						for continuation := index; continuation <= end; continuation++ {
							output = append(output, strings.TrimSuffix(lines[continuation], "\r"))
						}
						index = end
						continue
					}
					if seen[key] {
						index = end
						continue
					}
					for _, field := range required {
						if field.key == key {
							output = append(output, field.line)
							seen[key] = true
							break
						}
					}
					index = end
					continue
				}
			}
			skipLine := false
			for _, field := range required {
				if key != field.key {
					continue
				}
				if key == "args" && preserveExistingArgs {
					seen[key] = true
					break
				}
				if seen[key] {
					skipLine = true
				} else {
					lineWithoutCR = field.line
					seen[key] = true
				}
				break
			}
			if skipLine {
				continue
			}
		}
		output = append(output, lineWithoutCR)
	}
	if inSection {
		appendMissing()
	}
	if !found {
		if len(output) > 0 && strings.TrimSpace(output[len(output)-1]) != "" {
			output = append(output, "")
		}
		output = append(output, section, commandLine, argsLine, requiredLine, envLine, timeoutLine)
	}
	updated := strings.Join(output, "\n") + "\n"
	return updated, updated != contents, nil
}

func isWaypostSectionHeader(line, section string) bool {
	got, gotOK := canonicalTOMLHeader(line)
	want, wantOK := canonicalTOMLHeader(section)
	return gotOK && wantOK && got == want
}

func isWaypostTransportSubtable(line string) bool {
	header, ok := canonicalTOMLHeader(line)
	if !ok {
		return false
	}
	for _, suffix := range []string{"oauth", "http_headers", "env_http_headers", "http_headers_helper"} {
		prefix := "mcp_servers.waypost." + suffix
		if header == prefix || strings.HasPrefix(header, prefix+".") {
			return true
		}
	}
	return false
}

func canonicalTOMLHeader(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if len(line) < 2 || line[0] != '[' || line[1] == '[' {
		return "", false
	}
	closing := tomlHeaderEnd(line)
	if closing < 0 {
		return "", false
	}
	trailing := strings.TrimSpace(line[closing+1:])
	if trailing != "" && !strings.HasPrefix(trailing, "#") {
		return "", false
	}
	parts, ok := splitTOMLHeaderKeys(strings.TrimSpace(line[1:closing]))
	if !ok || len(parts) == 0 {
		return "", false
	}
	for index, part := range parts {
		part, ok = normalizeTOMLKeyPart(part)
		if !ok || part == "" {
			return "", false
		}
		parts[index] = part
	}
	return strings.Join(parts, "."), true
}

func tomlHeaderEnd(line string) int {
	var quote byte
	escaped := false
	for index := 1; index < len(line); index++ {
		value := line[index]
		if quote != 0 {
			if quote == '"' {
				if escaped {
					escaped = false
				} else if value == '\\' {
					escaped = true
				} else if value == quote {
					quote = 0
				}
			} else if value == quote {
				quote = 0
			}
			continue
		}
		if value == '"' || value == '\'' {
			quote = value
		} else if value == ']' {
			return index
		}
	}
	return -1
}

func splitTOMLHeaderKeys(value string) ([]string, bool) {
	parts := make([]string, 0, 2)
	start := 0
	var quote byte
	escaped := false
	for index := 0; index < len(value); index++ {
		current := value[index]
		if quote != 0 {
			if quote == '"' {
				if escaped {
					escaped = false
				} else if current == '\\' {
					escaped = true
				} else if current == quote {
					quote = 0
				}
			} else if current == quote {
				quote = 0
			}
			continue
		}
		if current == '"' || current == '\'' {
			quote = current
		} else if current == '.' {
			parts = append(parts, strings.TrimSpace(value[start:index]))
			start = index + 1
		}
	}
	if quote != 0 {
		return nil, false
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts, true
}

func normalizeTOMLKeyPart(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		decoded, err := strconv.Unquote(value)
		return decoded, err == nil
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], true
	}
	return value, true
}

func isInlineWaypostAssignment(line string) bool {
	// Inline tables need a real value parser to update without dropping unknown
	// fields. Refuse this form instead of appending a duplicate table.
	line = strings.TrimSpace(line)
	equal := strings.IndexByte(line, '=')
	if equal <= 0 {
		return false
	}
	key, ok := normalizeTOMLKeyPart(line[:equal])
	if !ok || key != serverName {
		return false
	}
	rhs := strings.TrimSpace(line[equal+1:])
	return strings.HasPrefix(rhs, "{")
}

func isInlineMCPServersAssignment(line string) bool {
	line = strings.TrimSpace(line)
	equal := strings.IndexByte(line, '=')
	if equal <= 0 {
		return false
	}
	key, ok := normalizeTOMLKeyPart(line[:equal])
	if !ok || key != "mcp_servers" {
		return false
	}
	rhs := strings.TrimSpace(line[equal+1:])
	return strings.HasPrefix(rhs, "{")
}

// tomlArrayEnd finds the end of an array value beginning on start. It only
// needs to recognize strings, comments, and nested arrays; all other TOML
// syntax is left untouched by the line-preserving updater.
func tomlArrayEnd(lines []string, start int) (end int, multiline, ok bool) {
	if start < 0 || start >= len(lines) {
		return start, false, false
	}
	first := strings.TrimSuffix(lines[start], "\r")
	equal := strings.IndexByte(first, '=')
	if equal < 0 {
		return start, false, true
	}
	rhs := strings.TrimSpace(first[equal+1:])
	if rhs == "" || rhs[0] != '[' {
		return start, false, true
	}

	depth := 0
	var quote byte
	triple := false
	escaped := false
	started := false
	for lineIndex := start; lineIndex < len(lines); lineIndex++ {
		line := strings.TrimSuffix(lines[lineIndex], "\r")
		lineStart := 0
		if lineIndex == start {
			lineStart = equal + 1
		}
		for index := lineStart; index < len(line); {
			value := line[index]
			if quote != 0 {
				if triple {
					if quote == '"' && strings.HasPrefix(line[index:], `"""`) {
						quote = 0
						triple = false
						index += 3
						continue
					}
					if quote == '\'' && strings.HasPrefix(line[index:], `'''`) {
						quote = 0
						triple = false
						index += 3
						continue
					}
					if quote == '"' && value == '\\' && index+1 < len(line) {
						index += 2
						continue
					}
					index++
					continue
				}
				if quote == '"' {
					if escaped {
						escaped = false
					} else if value == '\\' {
						escaped = true
					} else if value == '"' {
						quote = 0
					}
				} else if value == '\'' {
					quote = 0
				}
				index++
				continue
			}
			if value == '#' {
				break
			}
			if value == '"' || value == '\'' {
				quote = value
				if index+2 < len(line) && line[index+1] == value && line[index+2] == value {
					triple = true
					index += 3
				} else {
					index++
				}
				continue
			}
			if value == '[' {
				started = true
				depth++
			} else if value == ']' && started {
				depth--
				if depth < 0 {
					return lineIndex, lineIndex > start, false
				}
				if depth == 0 {
					return lineIndex, lineIndex > start, true
				}
			}
			index++
		}
	}
	return len(lines) - 1, len(lines)-1 > start, false
}

func mergeEnvVarsLine(lines []string, start, end int) (string, error) {
	if start < 0 || end < start || end >= len(lines) {
		return "", errors.New("invalid env_vars range")
	}
	first := strings.TrimSuffix(lines[start], "\r")
	equal := strings.IndexByte(first, '=')
	if equal < 0 || strings.TrimSpace(first[equal+1:]) == "" || strings.TrimSpace(first[equal+1:])[0] != '[' {
		return "", errors.New("env_vars must be an array")
	}
	values, err := parseTOMLStringArray(lines, start, end, equal)
	if err != nil {
		return "", err
	}
	for _, required := range requiredEnvVars {
		found := false
		for _, value := range values {
			if value == required {
				found = true
				break
			}
		}
		if !found {
			values = append(values, required)
		}
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = strconv.Quote(value)
	}
	return "env_vars = [" + strings.Join(quoted, ", ") + "]", nil
}

func parseTOMLStringArray(lines []string, start, end, equal int) ([]string, error) {
	values := make([]string, 0)
	depth := 0
	var quote byte
	escaped := false
	var raw strings.Builder
	for lineIndex := start; lineIndex <= end; lineIndex++ {
		line := strings.TrimSuffix(lines[lineIndex], "\r")
		lineStart := 0
		if lineIndex == start {
			lineStart = equal + 1
		}
		for index := lineStart; index < len(line); index++ {
			value := line[index]
			if quote != 0 {
				raw.WriteByte(value)
				if quote == '"' {
					if escaped {
						escaped = false
					} else if value == '\\' {
						escaped = true
					} else if value == '"' {
						decoded, err := strconv.Unquote(raw.String())
						if err != nil {
							return nil, err
						}
						values = append(values, decoded)
						quote = 0
						raw.Reset()
					}
				} else if value == '\'' {
					values = append(values, raw.String()[1:len(raw.String())-1])
					quote = 0
					raw.Reset()
				}
				continue
			}
			if value == '#' {
				break
			}
			switch value {
			case '"', '\'':
				if index+2 < len(line) && line[index+1] == value && line[index+2] == value {
					return nil, errors.New("triple-quoted env_vars are unsupported")
				}
				quote = value
				raw.WriteByte(value)
			case '[':
				depth++
			case ']':
				if depth == 0 {
					return nil, errors.New("unexpected closing bracket in env_vars")
				}
				depth--
			case ',', ' ', '\t':
				// Separators and whitespace are expected in a string array.
			default:
				if depth > 0 && value != '\r' {
					return nil, fmt.Errorf("env_vars contains an unquoted value")
				}
			}
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated string in env_vars")
	}
	if depth != 0 {
		return nil, errors.New("unterminated env_vars array")
	}
	return values, nil
}

func isMissingCodexMCPError(detail string) bool {
	detail = strings.ToLower(detail)
	singleQuoted := "no mcp server named '" + serverName + "' found"
	doubleQuoted := `no mcp server named "` + serverName + `" found`
	return strings.Contains(detail, singleQuoted) || strings.Contains(detail, doubleQuoted)
}

func commandError(action string, output commandOutput, err error) error {
	detail := strings.TrimSpace(string(output.stderr))
	if detail == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	runes := []rune(detail)
	if len(runes) > commandFailureLimit {
		detail = string(runes[:commandFailureLimit]) + "…"
	}
	return fmt.Errorf("%s: %w: %s", action, err, detail)
}
