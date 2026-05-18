// Package reaper finds and kills orphaned processes left behind in agent
// worktrees by claude agent runs (npm/next/etc. that detach into their own
// session and survive their parent shell).
package reaper

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Reaped describes a single process the reaper killed.
type Reaped struct {
	PID     int
	PPID    int
	Command string
	Age     time.Duration
	CPU     float64 // % of one core (top-style)
	Signal  string  // "TERM" or "KILL"
}

func (r Reaped) String() string {
	return fmt.Sprintf("pid=%d ppid=%d cmd=%s age=%s cpu=%.0f%% sig=%s",
		r.PID, r.PPID, r.Command, r.Age.Truncate(time.Second), r.CPU, r.Signal)
}

// Sweep finds every process whose current working directory is inside
// worktreePath and terminates it (SIGTERM, then SIGKILL after grace).
// Returns the list of processes that were reaped.
func Sweep(worktreePath string) ([]Reaped, error) {
	if worktreePath == "" {
		return nil, nil
	}
	pids, err := findPIDsInPath(worktreePath)
	if err != nil {
		return nil, err
	}
	return killPIDs(pids), nil
}

// SweepMany runs Sweep across multiple worktree paths and aggregates results.
func SweepMany(paths []string) []Reaped {
	var all []Reaped
	for _, p := range paths {
		r, err := Sweep(p)
		if err != nil {
			continue
		}
		all = append(all, r...)
	}
	return all
}

// findPIDsInPath enumerates PIDs whose cwd is inside worktreePath via lsof.
// Uses `lsof -d cwd -F pn`, which is one process spawn for the whole system.
func findPIDsInPath(worktreePath string) ([]int, error) {
	out, err := exec.Command("lsof", "-d", "cwd", "-F", "pn").Output()
	if err != nil {
		// lsof returns nonzero when some pids fail to inspect; output is still usable.
		if _, ok := err.(*exec.ExitError); !ok {
			return nil, fmt.Errorf("lsof: %w", err)
		}
	}
	// lsof reports resolved (real) paths; resolve our target the same way so
	// symlinked roots (e.g. /var/folders → /private/var/folders on macOS) match.
	resolved, rerr := filepath.EvalSymlinks(worktreePath)
	if rerr != nil {
		resolved = worktreePath
	}
	prefix := strings.TrimRight(resolved, "/") + "/"
	var pids []int
	var curPID int
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			n, err := strconv.Atoi(line[1:])
			if err == nil {
				curPID = n
			}
		case 'n':
			path := line[1:]
			if curPID == 0 {
				continue
			}
			if path == resolved || strings.HasPrefix(path, prefix) {
				pids = append(pids, curPID)
			}
		}
	}
	return pids, nil
}

// killPIDs sends SIGTERM, waits a short grace period, then escalates to SIGKILL
// for any survivors. Returns the list of reaped processes with metadata.
func killPIDs(pids []int) []Reaped {
	if len(pids) == 0 {
		return nil
	}
	// Snapshot metadata before killing so we can report on what we hit.
	meta := snapshotMeta(pids)
	var reaped []Reaped

	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			continue
		}
	}

	time.Sleep(2 * time.Second)

	for _, pid := range pids {
		m := meta[pid]
		if syscall.Kill(pid, 0) == nil {
			// Still alive — escalate.
			_ = syscall.Kill(pid, syscall.SIGKILL)
			m.Signal = "KILL"
		} else {
			m.Signal = "TERM"
		}
		m.PID = pid
		reaped = append(reaped, m)
	}
	return reaped
}

// snapshotMeta collects ppid/command/etime/cpu for a set of PIDs in one ps call.
func snapshotMeta(pids []int) map[int]Reaped {
	out := map[int]Reaped{}
	if len(pids) == 0 {
		return out
	}
	args := []string{"-o", "pid=,ppid=,etime=,%cpu=,comm="}
	for _, pid := range pids {
		args = append(args, "-p", strconv.Itoa(pid))
	}
	raw, err := exec.Command("ps", args...).Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		cpu, _ := strconv.ParseFloat(fields[3], 64)
		out[pid] = Reaped{
			PID:     pid,
			PPID:    ppid,
			Age:     parseEtime(fields[2]),
			CPU:     cpu,
			Command: strings.Join(fields[4:], " "),
		}
	}
	return out
}

// parseEtime parses ps etime format: [[DD-]HH:]MM:SS
func parseEtime(s string) time.Duration {
	var days, hours, mins, secs int
	if idx := strings.Index(s, "-"); idx >= 0 {
		days, _ = strconv.Atoi(s[:idx])
		s = s[idx+1:]
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3:
		hours, _ = strconv.Atoi(parts[0])
		mins, _ = strconv.Atoi(parts[1])
		secs, _ = strconv.Atoi(parts[2])
	case 2:
		mins, _ = strconv.Atoi(parts[0])
		secs, _ = strconv.Atoi(parts[1])
	case 1:
		secs, _ = strconv.Atoi(parts[0])
	}
	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(mins)*time.Minute +
		time.Duration(secs)*time.Second
}
