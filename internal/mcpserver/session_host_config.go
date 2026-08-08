package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SessionHostConfig is immutable operator-owned configuration for the generic
// session_create tool. It is loaded once before the MCP server starts and is
// intentionally not exposed through MCP results.
type SessionHostConfig struct {
	Profiles map[string]SessionHostProfile `json:"profiles"`
}

// SessionHostProfile maps one logical profile to the host-specific values
// required to create a session. AgentDeckCommand is trusted operator
// configuration; workflow prompts can only name the profile key.
type SessionHostProfile struct {
	AgentDeckCommand string `json:"agent_deck_command,omitempty"`
	ThurboxAgent     string `json:"thurbox_agent,omitempty"`
}

// LoadSessionHostConfig parses the explicit --session-host-config file. A
// caller passes the returned value to Options, so the configuration is fixed
// for the lifetime of the MCP server rather than being watched or reloaded.
func LoadSessionHostConfig(path string) (*SessionHostConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("session-host configuration path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("session-host configuration path must be absolute")
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session-host configuration: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var config SessionHostConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parse session-host configuration: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("parse session-host configuration: %w", err)
	}
	if config.Profiles == nil {
		return nil, fmt.Errorf("session-host configuration requires profiles")
	}

	profiles := make(map[string]SessionHostProfile, len(config.Profiles))
	for name, profile := range config.Profiles {
		if _, err := validateLogicalLaunchProfile(name); err != nil {
			return nil, fmt.Errorf("session-host configuration profile %q: %w", name, err)
		}
		profile.AgentDeckCommand = strings.TrimSpace(profile.AgentDeckCommand)
		profile.ThurboxAgent = strings.TrimSpace(profile.ThurboxAgent)
		if profile.AgentDeckCommand == "" && profile.ThurboxAgent == "" {
			return nil, fmt.Errorf("session-host configuration profile %q requires agent_deck_command or thurbox_agent", name)
		}
		profiles[name] = profile
	}
	return &SessionHostConfig{Profiles: profiles}, nil
}

func cloneSessionHostConfig(config *SessionHostConfig) *SessionHostConfig {
	if config == nil {
		return nil
	}
	profiles := make(map[string]SessionHostProfile, len(config.Profiles))
	for name, profile := range config.Profiles {
		profiles[name] = profile
	}
	return &SessionHostConfig{Profiles: profiles}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("contains multiple JSON values")
	}
	return err
}

func (config *SessionHostConfig) profileForHost(host sessionHost, profileName string) (string, error) {
	if config == nil {
		return "", fmt.Errorf("generic session creation requires session-host configuration")
	}
	profile, ok := config.Profiles[profileName]
	if !ok {
		return "", fmt.Errorf("unknown launch_profile %q", profileName)
	}
	switch host {
	case sessionHostAgentDeck:
		if profile.AgentDeckCommand == "" {
			return "", fmt.Errorf("launch_profile %q has no agent-deck mapping", profileName)
		}
		return profile.AgentDeckCommand, nil
	case sessionHostThurbox:
		if profile.ThurboxAgent == "" {
			return "", fmt.Errorf("launch_profile %q has no thurbox mapping", profileName)
		}
		return profile.ThurboxAgent, nil
	default:
		return "", fmt.Errorf("unsupported session host %q", host)
	}
}
