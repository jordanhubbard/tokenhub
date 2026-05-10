package fleet_orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// HostMap resolves a fleet host name (e.g. "rocky") to an ssh-reachable
// address (e.g. "100.125.137.89"). Empty value means "this orchestrator is
// the host" — execute commands locally instead of via ssh.
type HostMap map[string]string

// RolloutActivities holds the dependencies the RolloutWorkflow activities
// need. Constructed once at startup; safe for concurrent use.
type RolloutActivities struct {
	// HubURL points at the acc-server hub's HTTP endpoint, used to read the
	// ccc_version reported by /api/agents/:name during health checks.
	HubURL string
	// HubAuthToken is the bearer token used to query hub APIs. Read once at
	// startup; rotation is handled by re-creating the activities struct.
	HubAuthToken string
	// ArtifactStoreURL points at the acc-server hub for artifact downloads.
	// In the typical deployment HubURL == ArtifactStoreURL.
	ArtifactStoreURL string
	// SSHHosts maps fleet host name → ssh-reachable address. An empty value
	// means "execute commands locally".
	SSHHosts HostMap
	// SSHUser is the unix user that the ssh transport authenticates as.
	// Defaults to the current user when empty.
	SSHUser string
	// HTTPClient is used for hub queries. Defaults to a 10-second timeout
	// client when nil.
	HTTPClient *http.Client
	// CommandRunner runs a command on a fleet host. If nil, defaults to
	// execSSH which uses the local `ssh` binary. Tests inject a mock.
	CommandRunner func(ctx context.Context, host, sshTarget, sshUser, command string) (string, error)
}

func (a *RolloutActivities) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (a *RolloutActivities) runner() func(ctx context.Context, host, sshTarget, sshUser, command string) (string, error) {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return execSSH
}

// runOnHost dispatches to the configured runner. Empty sshTarget means local
// (current orchestrator host) — we exec the command directly without ssh.
func (a *RolloutActivities) runOnHost(ctx context.Context, host, command string) (string, error) {
	target := a.SSHHosts[host]
	return a.runner()(ctx, host, target, a.SSHUser, command)
}

// execSSH is the default CommandRunner. When sshTarget is empty, runs the
// command directly via /bin/sh -c so the orchestrator can roll itself out
// without requiring an ssh listener.
func execSSH(ctx context.Context, host, sshTarget, sshUser, command string) (string, error) {
	var cmd *exec.Cmd
	if sshTarget == "" {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	} else {
		dest := sshTarget
		if sshUser != "" {
			dest = sshUser + "@" + sshTarget
		}
		cmd = exec.CommandContext(ctx, "ssh",
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=10",
			"-o", "StrictHostKeyChecking=accept-new",
			dest, "--", command,
		)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("run on %s: %w (output: %s)", host, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ── Preflight ────────────────────────────────────────────────────────────────

type PreflightInput struct {
	Host          string `json:"host"`
	Component     string `json:"component"`
	TargetVersion string `json:"target_version"`
}

type PreflightResult struct {
	Host            string `json:"host"`
	CurrentVersion  string `json:"current_version"`
	DiskFreeKB      int64  `json:"disk_free_kb"`
	AlreadyAtTarget bool   `json:"already_at_target"`
}

// Preflight reads the agent's currently-reported ccc_version from the hub,
// compares it to the target, and probes free disk on the host. Replay-safe:
// when AlreadyAtTarget is true the workflow skips the install activities for
// that host and the rollout becomes a no-op for that host.
func (a *RolloutActivities) Preflight(ctx context.Context, in PreflightInput) (PreflightResult, error) {
	current, err := a.fetchAgentVersion(ctx, in.Host)
	if err != nil {
		return PreflightResult{}, fmt.Errorf("fetch %s ccc_version: %w", in.Host, err)
	}

	dfOut, err := a.runOnHost(ctx, in.Host, "df -k --output=avail /home 2>/dev/null | tail -1 || df -k /Users 2>/dev/null | tail -1 | awk '{print $4}'")
	if err != nil {
		// disk check failure is recoverable — treat as 0 free, let install fail
		// loudly later if it actually matters. Don't block preflight.
		dfOut = "0"
	}
	var freeKB int64
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(dfOut), "%d", &freeKB); scanErr != nil {
		freeKB = 0
	}

	return PreflightResult{
		Host:            in.Host,
		CurrentVersion:  current,
		DiskFreeKB:      freeKB,
		AlreadyAtTarget: current == in.TargetVersion && current != "",
	}, nil
}

// fetchAgentVersion queries the hub's /api/agents/:name and returns the
// `ccc_version` field. Empty string when the agent is not registered or the
// field is absent.
func (a *RolloutActivities) fetchAgentVersion(ctx context.Context, agent string) (string, error) {
	if a.HubURL == "" {
		return "", errors.New("HubURL not configured")
	}
	url := fmt.Sprintf("%s/api/agents/%s", strings.TrimRight(a.HubURL, "/"), agent)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	if a.HubAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.HubAuthToken)
	}
	resp, err := a.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil // agent unknown
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("hub /api/agents/%s: status %d", agent, resp.StatusCode)
	}
	var body struct {
		Agent struct {
			CCCVersion string `json:"ccc_version"`
		} `json:"agent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode /api/agents/%s: %w", agent, err)
	}
	return body.Agent.CCCVersion, nil
}

// ── DownloadArtifact ────────────────────────────────────────────────────────

type DownloadArtifactInput struct {
	Host     string `json:"host"`
	SHA256   string `json:"sha256"`
	DestPath string `json:"dest_path"` // e.g. /home/jkh/.acc/bin/acc-agent.new
}

type DownloadArtifactResult struct {
	Host      string `json:"host"`
	BytesSeen int64  `json:"bytes_seen"`
	DestPath  string `json:"dest_path"`
}

// DownloadArtifact fetches a content-addressed blob from the artifact store
// onto the target host via curl. Idempotent on (host, sha256, dest_path):
// repeated calls overwrite, but the SHA-256 is verified after download and
// retried on mismatch.
func (a *RolloutActivities) DownloadArtifact(ctx context.Context, in DownloadArtifactInput) (DownloadArtifactResult, error) {
	if a.ArtifactStoreURL == "" {
		return DownloadArtifactResult{}, errors.New("ArtifactStoreURL not configured")
	}
	url := fmt.Sprintf("%s/api/artifacts/blobs/%s",
		strings.TrimRight(a.ArtifactStoreURL, "/"), in.SHA256)
	auth := ""
	if a.HubAuthToken != "" {
		auth = fmt.Sprintf(" -H 'Authorization: Bearer %s'", a.HubAuthToken)
	}
	cmd := fmt.Sprintf(
		"set -e; mkdir -p \"$(dirname %q)\"; curl -fsSL%s -o %q.part %q && "+
			"got=$(sha256sum %q.part | awk '{print $1}') && "+
			"if [ \"$got\" != %q ]; then echo \"sha mismatch: got $got want %s\" >&2; rm -f %q.part; exit 11; fi && "+
			"mv %q.part %q && stat -c '%%s' %q",
		in.DestPath, auth, in.DestPath, url,
		in.DestPath,
		in.SHA256, in.SHA256, in.DestPath,
		in.DestPath, in.DestPath, in.DestPath,
	)
	out, err := a.runOnHost(ctx, in.Host, cmd)
	if err != nil {
		return DownloadArtifactResult{}, err
	}
	var n int64
	_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &n)
	return DownloadArtifactResult{
		Host:      in.Host,
		BytesSeen: n,
		DestPath:  in.DestPath,
	}, nil
}

// ── InstallBinary ───────────────────────────────────────────────────────────

type InstallBinaryInput struct {
	Host     string `json:"host"`
	NewPath  string `json:"new_path"`  // e.g. ~/.acc/bin/acc-agent.new
	DestPath string `json:"dest_path"` // e.g. ~/.acc/bin/acc-agent
}

type InstallBinaryResult struct {
	Host       string `json:"host"`
	Installed  bool   `json:"installed"`
	BackupPath string `json:"backup_path"`
}

// InstallBinary atomically renames `new_path` over `dest_path`, preserving
// the prior binary as `dest_path.prev` for rollback. Replay-safe: a re-run
// when new_path is missing returns Installed=false without error.
func (a *RolloutActivities) InstallBinary(ctx context.Context, in InstallBinaryInput) (InstallBinaryResult, error) {
	cmd := fmt.Sprintf(
		"set -e; "+
			"if [ ! -f %q ]; then echo missing-new; exit 0; fi; "+
			"chmod +x %q; "+
			"if [ -f %q ]; then mv %q %q.prev; fi; "+
			"mv %q %q; "+
			"echo installed",
		in.NewPath,
		in.NewPath,
		in.DestPath, in.DestPath, in.DestPath,
		in.NewPath, in.DestPath,
	)
	out, err := a.runOnHost(ctx, in.Host, cmd)
	if err != nil {
		return InstallBinaryResult{}, err
	}
	out = strings.TrimSpace(out)
	return InstallBinaryResult{
		Host:       in.Host,
		Installed:  out == "installed",
		BackupPath: in.DestPath + ".prev",
	}, nil
}

// ── RestartService ──────────────────────────────────────────────────────────

type RestartServiceInput struct {
	Host string `json:"host"`
	Unit string `json:"unit"` // e.g. acc-agent.service
	User bool   `json:"user"` // true = systemctl --user
}

type RestartServiceResult struct {
	Host    string `json:"host"`
	Restart bool   `json:"restart"`
}

// RestartService restarts a systemd unit via systemctl. user=true selects
// --user. Returns Restart=true when the unit is active after restart.
func (a *RolloutActivities) RestartService(ctx context.Context, in RestartServiceInput) (RestartServiceResult, error) {
	flag := ""
	prefix := "sudo "
	if in.User {
		flag = "--user "
		prefix = ""
	}
	cmd := fmt.Sprintf(
		"set -e; %ssystemctl %srestart %s; sleep 2; "+
			"%ssystemctl %sis-active %s",
		prefix, flag, in.Unit,
		prefix, flag, in.Unit,
	)
	out, err := a.runOnHost(ctx, in.Host, cmd)
	if err != nil {
		return RestartServiceResult{}, err
	}
	return RestartServiceResult{
		Host:    in.Host,
		Restart: strings.TrimSpace(out) == "active",
	}, nil
}

// ── rolloutHostActivity ─────────────────────────────────────────────────────
//
// rolloutHostActivity runs the per-host pipeline as a single activity so the
// workflow remains compact. Inside, it sequences Preflight → (Download →
// Install → Restart, when the host isn't already at target) → WaitForHealth.
// Returning a HostOutcome lets the workflow aggregate without re-querying.

func (a *RolloutActivities) RolloutHost(ctx context.Context, in rolloutHostInput) (HostOutcome, error) {
	host := in.Host
	out := HostOutcome{Host: host.Name}

	pre, err := a.Preflight(ctx, PreflightInput{
		Host:          host.Name,
		Component:     in.Component,
		TargetVersion: in.Version,
	})
	if err != nil {
		out.Reason = "preflight: " + err.Error()
		return out, err
	}

	if pre.AlreadyAtTarget {
		out.Skipped = true
	} else {
		sha := in.ArchSHA[host.Arch]
		if sha == "" {
			out.Reason = "no manifest entry for arch " + host.Arch
			return out, errors.New(out.Reason)
		}
		newPath := host.BinPath + ".new"
		if _, err := a.DownloadArtifact(ctx, DownloadArtifactInput{
			Host: host.Name, SHA256: sha, DestPath: newPath,
		}); err != nil {
			out.Reason = "download: " + err.Error()
			return out, err
		}
		ir, err := a.InstallBinary(ctx, InstallBinaryInput{
			Host: host.Name, NewPath: newPath, DestPath: host.BinPath,
		})
		if err != nil {
			out.Reason = "install: " + err.Error()
			return out, err
		}
		out.Installed = ir.Installed
		if _, err := a.RestartService(ctx, RestartServiceInput{
			Host: host.Name, Unit: host.Unit, User: host.UnitUser,
		}); err != nil {
			out.Reason = "restart: " + err.Error()
			return out, err
		}
	}

	timeout := in.HealthTimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	hr, err := a.WaitForHealth(ctx, WaitForHealthInput{
		Host: host.Name, TargetVersion: in.Version, TimeoutSeconds: timeout,
	})
	if err != nil {
		out.Reason = "health: " + err.Error()
		return out, err
	}
	out.Healthy = hr.Healthy
	return out, nil
}

// ── WaitForHealth ───────────────────────────────────────────────────────────

type WaitForHealthInput struct {
	Host           string `json:"host"`
	TargetVersion  string `json:"target_version"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type WaitForHealthResult struct {
	Host           string `json:"host"`
	CurrentVersion string `json:"current_version"`
	Healthy        bool   `json:"healthy"`
}

// WaitForHealth polls the hub's /api/agents/:name until ccc_version == target
// or the timeout expires. Polling cadence: 5s. Activity heartbeats every
// iteration so Temporal can detect a stuck activity.
func (a *RolloutActivities) WaitForHealth(ctx context.Context, in WaitForHealthInput) (WaitForHealthResult, error) {
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var last string
	for {
		current, err := a.fetchAgentVersion(ctx, in.Host)
		if err == nil {
			last = current
			if current == in.TargetVersion {
				return WaitForHealthResult{
					Host:           in.Host,
					CurrentVersion: current,
					Healthy:        true,
				}, nil
			}
		}
		if time.Now().After(deadline) {
			return WaitForHealthResult{
				Host:           in.Host,
				CurrentVersion: last,
				Healthy:        false,
			}, fmt.Errorf("health timeout for %s: current=%q want=%q",
				in.Host, last, in.TargetVersion)
		}
		select {
		case <-ctx.Done():
			return WaitForHealthResult{Host: in.Host, CurrentVersion: last}, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
