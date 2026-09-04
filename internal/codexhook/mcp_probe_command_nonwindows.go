//go:build !windows

package codexhook

func mcpProbeInvocation(args ...string) (string, []string) {
	return "codex", args
}
