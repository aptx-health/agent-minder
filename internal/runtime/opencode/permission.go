package opencode

import "strings"

// permissionEnvKey is the environment variable opencode reads to override its
// tool-permission policy. Its value is the inline JSON permission object (the
// same shape as the `permission` config key).
const permissionEnvKey = "OPENCODE_PERMISSION"

// defaultHeadlessPermission is the OPENCODE_PERMISSION policy minder injects into
// the shared serve process when the operator has not set one. minder runs
// headless-autonomous: there is no interactive approver, so any action opencode
// would otherwise gate must be pre-authorized, and any action that would stall
// waiting for a human must instead resolve deterministically.
//
// Verified against opencode 1.18.11 (`opencode debug config` / `opencode debug
// agent <name>`), which compiles this into last-match-wins rules:
//   - edit/write/bash/webfetch = allow — the agent can do real coding work.
//   - external_directory = deny — a hard worktree boundary. Each session is
//     scoped to its worktree via the `directory` param; opencode re-appends its
//     own tool-output tmp allow AFTER this rule, so its scratch space still works
//     while escapes outside the worktree are denied (not stalled on an "ask").
//   - doom_loop = allow — never stall on loop detection; max_turns / max_budget
//     bound runaway instead.
//   - question = deny — a headless agent cannot wait for a human answer, so deny
//     lets it proceed or bail rather than hang.
//
// Unlisted permissions (read, plan_*, todowrite, …) keep opencode's defaults.
const defaultHeadlessPermission = `{"edit":"allow","write":"allow","bash":"allow","webfetch":"allow","external_directory":"deny","doom_loop":"allow","question":"deny"}`

// withHeadlessPermission appends the default OPENCODE_PERMISSION policy to a
// process environment slice unless it is already set — by the operator's shell
// environment or an explicit Invocation.Env entry. Operator-supplied policy
// always wins; minder never overrides an explicit permission decision, honoring
// the "still honor explicit deny" guard.
func withHeadlessPermission(env []string) []string {
	prefix := permissionEnvKey + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return env
		}
	}
	return append(env, prefix+defaultHeadlessPermission)
}
