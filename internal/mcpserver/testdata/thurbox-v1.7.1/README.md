# Thurbox v1.7.1 session CLI fixtures

These fixtures are pinned to Thurbox commit
`a009e19ccfc71c54fbeaf120cb2267737fc90f4d` (release `1.7.1`), from
`github.com/Thurbeen/thurbox`.

The shapes come directly from:

- `src/cli/sessions.rs`: `session_json_with_state`, `shared_session_to_json`,
  and the `Create` and `Restart` JSON results;
- `src/session/mod.rs`: the `HOOK_STATES` vocabulary.

They intentionally cover only the grammar consumed by Waypost: `session get`,
`session list`, `session create`, and `session restart`. The adapter rejects
missing, extra, or unclassified fields rather than guessing from similarly
named fields such as `repo_path` or `worktree_path`.
