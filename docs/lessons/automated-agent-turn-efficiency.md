# Automated Agent Turn Efficiency

## What happened

An automated agent implementing issue #648 stopped after 100 turns and $5.04. The implementation was nearly complete, but the run spent too many turns on serialized inspection and a failed golden-file workflow.

The run used 99 tool calls:

- 32 calls for repository and issue exploration.
- 36 calls in the golden-file phase alone.
- 22 shell commands that invoked `go test`.
- 13 commands denied by the sandbox.

The expensive loop repeatedly tried small variations of commands such as:

```sh
GOLDEN_UPDATE=1 go test ./internal/daemon/... -run TestV1ReadEndpoints_Golden
go test ... > /tmp/goldenraw.txt
go test ... | tee /tmp/goldenout.txt | tail -200
```

After those commands were denied or ineffective, the agent finally created seven golden files with seven individual write calls. Targeted tests passed near the end of the run, but the budget expired while it was editing the changelog, before it could commit or open a PR.

## How to prevent it

### Agent design

- Give phases explicit tool-call budgets. For example: discovery 15, implementation 35, tests 25, publication 10, reserve 15.
- Add a denial circuit breaker: after one repeated permission failure, stop varying the same command and switch tools or request approval.
- Batch independent reads and searches. Prefer one `rg` query or parallel read group over many serial `grep`, `find`, and `Read` calls.
- Reserve enough turns for final validation, commit, push, and PR creation.
- Report progress at 50%, 75%, and 90% of the turn or cost budget so the supervisor can simplify or hand off.

### Skill design

Create a small golden-test skill that requires this sequence:

1. Prefer one combined golden document over many files when the contract allows it.
2. Use the file-editing tool for repository files; do not generate them with shell loops or redirection.
3. If update mode is supported, use one documented command and request approval once when required.
4. Run the targeted golden test once, inspect the complete failure, write the expected output, then rerun once.
5. Stop after two failed attempts and choose a different strategy.

The same skill should document sandbox-safe commands and prohibit retries that only rearrange pipes, environment assignments, or redirections.

### General guidance

- Use a test ladder: compile once, targeted tests once, full suite once, and rerun only failures.
- Avoid repeatedly truncating test output with different `head`, `tail`, `grep`, or `sed` combinations. Capture one useful result.
- Keep prompts and tool output compact. In this run, every extra turn reread a large cached context, so low-value retries also accelerated cost exhaustion.
- Treat a working implementation as a checkpoint: commit or preserve it before optional documentation and cleanup work.
