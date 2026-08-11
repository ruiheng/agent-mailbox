# Workspace Instructions

## Tool Caches

- Use the standard user-level cache for build and development tools, such as Go's default `GOCACHE` under `~/.cache/go-build`.
- If the sandbox cannot write to the user-level cache, request user approval to run the command with the required access. Do not silently redirect a persistent cache to `/tmp` or into the repository as a workaround.
- If an isolated temporary cache is explicitly necessary, create a unique directory with `mktemp -d` and remove it in the same command or workflow. Never use a fixed shared temporary-cache path across sessions.
- Do not leave tool caches, build artifacts, or temporary directories behind after the task.
