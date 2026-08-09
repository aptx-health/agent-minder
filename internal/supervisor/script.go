package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aptx-health/agent-minder/internal/db"
	"github.com/jmoiron/sqlx"
)

func runScriptJob(ctx context.Context, sc *SlotContext) error {
	job := sc.Job
	if !job.ScriptCommand.Valid || job.ScriptCommand.String == "" {
		detail := "script job is missing command"
		sc.EmitDurableWith(EventError, fmt.Sprintf("Script failed on %s: %s", sc.JobLabel(), detail),
			func(tx *sqlx.Tx) error {
				if err := db.UpdateJobFailureTx(tx, job.ID, "script_config", detail); err != nil {
					return err
				}
				return db.CompleteJobTx(tx, job.ID, db.StatusFailed)
			})
		return fmt.Errorf("script config: %s", detail)
	}

	logFile, err := sc.OpenLogFile(false)
	if err != nil {
		sc.EmitDurableWith(EventError, fmt.Sprintf("Log file error for %s: %v", sc.JobLabel(), err),
			func(tx *sqlx.Tx) error {
				if err := db.UpdateJobFailureTx(tx, job.ID, "log", err.Error()); err != nil {
					return err
				}
				return db.CompleteJobTx(tx, job.ID, db.StatusFailed)
			})
		return err
	}
	defer func() { _ = logFile.Close() }()
	_ = sc.Store.UpdateJobLog(job.ID, sc.LogPath)

	runID := beginScriptRun(sc)
	res := db.AgentRunResult{Status: db.RunStatusSuccess}
	defer func() {
		if runID != 0 {
			if sc.sup != nil {
				res.StepCount = sc.sup.endAgentRunTracking(sc.Job.ID)
			}
			_ = sc.Store.CompleteAgentRun(runID, res)
		}
	}()

	_, _ = fmt.Fprintf(logFile, "--- SCRIPT JOB %s ---\ncommand: %s\n\n", sc.JobLabel(), job.ScriptCommand.String)

	workDir := scriptWorkDir(sc)
	env, err := scriptEnv(job.ScriptEnv)
	if err != nil {
		res.Status = db.RunStatusFailed
		res.StopReason = "script_env"
		res.FailureDetail = err.Error()
		return completeScriptFailure(sc, "script_env", err.Error())
	}

	runCtx := ctx
	cancel := func() {}
	timeoutText := ""
	if job.ScriptTimeout.Valid && job.ScriptTimeout.String != "" {
		timeout, parseErr := time.ParseDuration(job.ScriptTimeout.String)
		if parseErr != nil {
			res.Status = db.RunStatusFailed
			res.StopReason = "timeout_config"
			res.FailureDetail = parseErr.Error()
			return completeScriptFailure(sc, "timeout_config", parseErr.Error())
		}
		timeoutText = timeout.String()
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", job.ScriptCommand.String)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		detail := fmt.Sprintf("script timed out after %s and was killed", timeoutText)
		res.Status = db.RunStatusFailed
		res.StopReason = "timeout"
		res.FailureDetail = detail
		return completeScriptFailure(sc, "timeout", detail)
	}
	if err != nil {
		exitCode := scriptExitCode(err)
		detail := fmt.Sprintf("script exited with code %d", exitCode)
		if exitCode < 0 {
			detail = fmt.Sprintf("script failed: %v", err)
		}
		res.Status = db.RunStatusFailed
		res.StopReason = "process_exit"
		res.FailureDetail = detail
		return completeScriptFailure(sc, "process_exit", detail)
	}

	sc.EmitDurableWith(EventCompleted, fmt.Sprintf("Script completed %s", sc.JobLabel()),
		func(tx *sqlx.Tx) error { return db.CompleteJobTx(tx, job.ID, db.StatusDone) })
	return nil
}

func beginScriptRun(sc *SlotContext) int64 {
	run := &db.AgentRun{
		JobID:   sc.Job.ID,
		Stage:   "script",
		Attempt: 1,
		Agent:   db.JobKindScript,
		Status:  db.RunStatusRunning,
		LogPath: sql.NullString{String: sc.LogPath, Valid: true},
	}
	if err := sc.Store.StartAgentRun(run); err != nil {
		debugLog("script run tracking failed", "job", sc.Job.ID, "error", err.Error())
		return 0
	}
	if sc.sup != nil {
		sc.sup.beginAgentRunTracking(sc.Job.ID, run.ID)
	}
	return run.ID
}

func completeScriptFailure(sc *SlotContext, reason, detail string) error {
	sc.EmitDurableWith(EventError, fmt.Sprintf("Script failed on %s: %s", sc.JobLabel(), detail),
		func(tx *sqlx.Tx) error {
			if err := db.UpdateJobFailureTx(tx, sc.Job.ID, reason, detail); err != nil {
				return err
			}
			return db.CompleteJobTx(tx, sc.Job.ID, db.StatusFailed)
		})
	return fmt.Errorf("%s: %s", reason, detail)
}

func scriptWorkDir(sc *SlotContext) string {
	if !sc.Job.ScriptWorkDir.Valid || sc.Job.ScriptWorkDir.String == "" {
		return sc.RepoDir
	}
	if filepath.IsAbs(sc.Job.ScriptWorkDir.String) {
		return sc.Job.ScriptWorkDir.String
	}
	return filepath.Join(sc.RepoDir, sc.Job.ScriptWorkDir.String)
}

func scriptEnv(raw sql.NullString) (map[string]string, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	env := map[string]string{}
	if err := json.Unmarshal([]byte(raw.String), &env); err != nil {
		return nil, fmt.Errorf("decode script env: %w", err)
	}
	return env, nil
}

func scriptExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
