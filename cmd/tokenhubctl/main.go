package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

var version = "dev"

// loadEnvFile reads ~/.tokenhub/env (written by make start) and sets any
// key=value pairs not already present in the process environment. This lets
// tokenhubctl work out of the box without shell profile configuration.
func loadEnvFile() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(home + "/.tokenhub/env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if os.Getenv(strings.TrimSpace(k)) == "" {
			_ = os.Setenv(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
}

func main() {
	loadEnvFile()
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version", "--version", "-v":
		fmt.Printf("tokenhubctl %s\n", version)
	case "admin-token":
		doAdminToken()
	case "rotate-admin-token":
		doRotateAdminToken(args)
	case "status":
		doStatus()
	case "health":
		doHealth()
	case "vault":
		doVault(args)
	case "provider", "providers":
		doProviders(args)
	case "model", "models":
		doModels(args)
	case "routing":
		doRouting(args)
	case "apikey", "apikeys":
		doAPIKeys(args)
	case "logs":
		doLogs(args)
	case "audit":
		doAudit(args)
	case "rewards":
		doRewards(args)
	case "stats":
		doStats()
	case "engine":
		doEngine(args)
	case "events":
		doEvents()
	case "discover":
		doDiscover(args)
	case "model-test":
		doModelTest(args)
	case "provider-status":
		doProviderStatus(args)
	case "simulate":
		doSimulate(args)
	case "tsdb":
		doTSDB(args)
	case "help", "--help", "-h":
		usageTo(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	usageTo(os.Stderr)
}

func usageTo(w io.Writer) {
	_, _ = fmt.Fprintf(w, `tokenhubctl — CLI for the TokenHub admin API

Usage: tokenhubctl <command> [arguments]

Environment:
  TOKENHUB_URL          Base URL (default: http://localhost:8090)
  TOKENHUB_ADMIN_TOKEN  Bearer token for admin endpoints

  ~/.tokenhub/env       Auto-sourced on startup; written by make start.
                        Explicit environment variables take precedence.

Commands:
  admin-token                 Print the admin token (env, file, or Docker)
  rotate-admin-token [token]   Rotate admin token (random if no token given)
  status                      Show server info and vault state
  health                      Show provider health stats

  vault unlock <password>     Unlock the vault
  vault lock                  Lock the vault
  vault rotate <old> <new>    Rotate the vault password

  provider list               List all providers (store + runtime)
  provider add <json>         Create or update a provider
  provider edit <id> <json>   Patch a provider
  provider delete <id>        Delete a provider
  provider discover <id>      Discover models from a provider

  model list                  List all models (store + runtime)
  model add <json>            Create or update a model
  model edit <id> <json>      Patch a model (weight, pricing, enabled)
  model delete <id>           Delete a model
  model enable <id>           Enable a model
  model disable <id>          Disable a model

  routing get                 Show routing config
  routing set <json>          Update routing config

  apikey list                 List API keys
  apikey create <json>        Create a new API key
  apikey rotate <id>          Rotate an API key
  apikey edit <id> <json>     Patch an API key
  apikey delete <id>          Delete an API key

  logs [--limit N]            Show request logs
  audit [--limit N]           Show audit logs
  rewards [--limit N]         Show reward entries
  stats                       Show aggregated stats
  engine models               Show runtime engine models and adapters
  events                      Stream real-time SSE events

  model-test <id> [api-key]   Send a test inference request through a model
  provider-status <id>        Show full health details for one provider

  simulate <json>             Run a what-if routing simulation
  tsdb query <args>           Query TSDB
  tsdb metrics                List TSDB metrics
  tsdb prune                  Prune old TSDB data

  version                     Show version
  help                        Show this help

Examples:
  tokenhubctl status
  tokenhubctl vault unlock "my-secret-password"
  tokenhubctl provider add '{"id":"openai","type":"openai","base_url":"https://api.openai.com","api_key":"sk-..."}'
  tokenhubctl model list
  tokenhubctl model edit gpt-4o '{"weight":9}'
  tokenhubctl routing set '{"default_mode":"cheap","default_max_budget_usd":0.02}'
  tokenhubctl apikey create '{"name":"my-app","scopes":"[\"chat\",\"plan\"]"}'
  tokenhubctl events
`)
}

// --- HTTP helpers ---

func baseURL() string {
	if u := os.Getenv("TOKENHUB_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://localhost:8090"
}

func adminToken() string {
	return os.Getenv("TOKENHUB_ADMIN_TOKEN")
}

func doRequest(method, path string, body io.Reader) (*http.Response, error) {
	url := baseURL() + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok := adminToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return http.DefaultClient.Do(req)
}

func doGet(path string) map[string]any {
	resp, err := doRequest("GET", path, nil)
	fatal(err)
	defer func() { _ = resp.Body.Close() }()
	return readJSON(resp)
}

func doPost(path, bodyJSON string) map[string]any {
	resp, err := doRequest("POST", path, strings.NewReader(bodyJSON))
	fatal(err)
	defer func() { _ = resp.Body.Close() }()
	return readJSON(resp)
}

func doPatch(path, bodyJSON string) map[string]any {
	resp, err := doRequest("PATCH", path, strings.NewReader(bodyJSON))
	fatal(err)
	defer func() { _ = resp.Body.Close() }()
	return readJSON(resp)
}

func doPut(path, bodyJSON string) map[string]any {
	resp, err := doRequest("PUT", path, strings.NewReader(bodyJSON))
	fatal(err)
	defer func() { _ = resp.Body.Close() }()
	return readJSON(resp)
}

func doDelete(path string) map[string]any {
	resp, err := doRequest("DELETE", path, nil)
	fatal(err)
	defer func() { _ = resp.Body.Close() }()
	return readJSON(resp)
}

func readJSON(resp *http.Response) map[string]any {
	data, err := io.ReadAll(resp.Body)
	fatal(err)
	if resp.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "HTTP %d: %s\n", resp.StatusCode, string(data))
		os.Exit(1)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		// Might be an array; wrap it.
		var arr []any
		if err2 := json.Unmarshal(data, &arr); err2 == nil {
			return map[string]any{"items": arr}
		}
		fmt.Println(string(data))
		os.Exit(0)
	}
	return result
}

func prettyJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func requireArgs(args []string, min int, usage string) {
	if len(args) < min {
		fmt.Fprintf(os.Stderr, "usage: tokenhubctl %s\n", usage)
		os.Exit(1)
	}
}

func parseLimit(args []string) int {
	for i, a := range args {
		if a == "--limit" && i+1 < len(args) {
			n, _ := strconv.Atoi(args[i+1])
			if n > 0 {
				return n
			}
		}
	}
	return 50
}

// --- Commands ---

func doAdminToken() {
	// 1. Environment variable.
	if tok := os.Getenv("TOKENHUB_ADMIN_TOKEN"); tok != "" {
		fmt.Println(tok)
		return
	}

	// 2. Local token file (native deployment).
	home, _ := os.UserHomeDir()
	if home != "" {
		if data, err := os.ReadFile(home + "/.tokenhub/.admin-token"); err == nil {
			if tok := strings.TrimSpace(string(data)); tok != "" {
				fmt.Println(tok)
				return
			}
		}
	}

	// 3. Docker container token file.
	for _, name := range []string{"tokenhub-tokenhub-1", "tokenhub"} {
		out, err := exec.Command("docker", "exec", name, "cat", "/data/.admin-token").Output()
		if err == nil {
			if tok := strings.TrimSpace(string(out)); tok != "" {
				fmt.Println(tok)
				return
			}
		}
	}

	fmt.Fprintln(os.Stderr, "admin token not found — set TOKENHUB_ADMIN_TOKEN or ensure the service is running")
	os.Exit(1)
}

func doRotateAdminToken(args []string) {
	var body string
	if len(args) > 0 {
		body = `{"token":"` + args[0] + `"}`
	} else {
		body = "{}"
	}
	result := doPost("/admin/v1/admin-token/rotate", body)
	ok, _ := result["ok"].(bool)
	token, _ := result["token"].(string)
	if !ok || token == "" {
		fmt.Fprintln(os.Stderr, "rotation failed:", result)
		os.Exit(1)
	}
	fmt.Println("Admin token rotated.")
	fmt.Println("New token:", token)
	fmt.Println()
	fmt.Println("Update your environment or run: make _write-env")
}

func doStatus() {
	info := doGet("/admin/v1/info")
	healthResp, err := doRequest("GET", "/healthz", nil)
	fatal(err)
	defer func() { _ = healthResp.Body.Close() }()
	hData, _ := io.ReadAll(healthResp.Body)
	var h map[string]any
	_ = json.Unmarshal(hData, &h)

	vaultState := "locked"
	if info["vault_locked"] == false {
		vaultState = "unlocked"
	}
	vaultInit := "no"
	if info["vault_initialized"] == true {
		vaultInit = "yes"
	}
	status := "unknown"
	if s, ok := h["status"].(string); ok {
		status = s
	}
	adapters := 0
	if n, ok := h["adapters"].(float64); ok {
		adapters = int(n)
	}
	models := 0
	if n, ok := h["models"].(float64); ok {
		models = int(n)
	}

	fmt.Printf("Server:           %s\n", baseURL())
	fmt.Printf("Status:           %s\n", status)
	fmt.Printf("Adapters:         %d\n", adapters)
	fmt.Printf("Models:           %d\n", models)
	fmt.Printf("Vault:            %s\n", vaultState)
	fmt.Printf("Vault initialized: %s\n", vaultInit)
}

func doHealth() {
	data := doGet("/admin/v1/health")
	providers, _ := data["providers"].([]any)
	if len(providers) == 0 {
		fmt.Println("No provider health data available.")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PROVIDER\tSTATE\tCONSEC_ERR\tAVG LATENCY\tLAST SUCCESS\tLAST ERROR")
	for _, p := range providers {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["provider_id"].(string)
		state, _ := m["state"].(string)
		errs := fmtNum(m["consec_errors"])
		lat := fmtDuration(m["avg_latency_ms"])
		lastOK := fmtTime(m["last_success_at"])
		lastErr, _ := m["last_error"].(string)
		if len(lastErr) > 60 {
			lastErr = lastErr[:57] + "..."
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", id, state, errs, lat, lastOK, lastErr)
	}
	_ = tw.Flush()
}

func doVault(args []string) {
	requireArgs(args, 1, "vault <unlock|lock|rotate> [args]")
	switch args[0] {
	case "unlock":
		requireArgs(args, 2, "vault unlock <password>")
		body := fmt.Sprintf(`{"admin_password":%s}`, jsonStr(args[1]))
		result := doPost("/admin/v1/vault/unlock", body)
		if result["ok"] == true {
			fmt.Println("Vault unlocked.")
		}
	case "lock":
		result := doPost("/admin/v1/vault/lock", "{}")
		if result["ok"] == true {
			if result["already_locked"] == true {
				fmt.Println("Vault was already locked.")
			} else {
				fmt.Println("Vault locked.")
			}
		}
	case "rotate":
		requireArgs(args, 3, "vault rotate <old-password> <new-password>")
		body := fmt.Sprintf(`{"old_password":%s,"new_password":%s}`, jsonStr(args[1]), jsonStr(args[2]))
		result := doPost("/admin/v1/vault/rotate", body)
		if result["ok"] == true {
			fmt.Println("Vault password rotated.")
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown vault command: %s\n", args[0])
		os.Exit(1)
	}
}

func doProviders(args []string) {
	if len(args) == 0 || args[0] == "list" {
		data := doGet("/admin/v1/engine/models")
		adapters, _ := data["adapter_info"].([]any)
		models, _ := data["models"].([]any)

		storeData := doGet("/admin/v1/providers")
		storeItems, _ := storeData["items"].([]any)
		storeMap := map[string]map[string]any{}
		for _, p := range storeItems {
			m, _ := p.(map[string]any)
			if id, ok := m["id"].(string); ok {
				storeMap[id] = m
			}
		}

		adapterMap := map[string]string{}
		for _, a := range adapters {
			m, _ := a.(map[string]any)
			id, _ := m["id"].(string)
			ep, _ := m["health_endpoint"].(string)
			adapterMap[id] = ep
		}

		allIDs := map[string]bool{}
		for id := range storeMap {
			allIDs[id] = true
		}
		for id := range adapterMap {
			allIDs[id] = true
		}
		for _, m := range models {
			mm, _ := m.(map[string]any)
			if pid, ok := mm["provider_id"].(string); ok {
				allIDs[pid] = true
			}
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tTYPE\tBASE URL\tCREDS\tENABLED\tMODELS\tSOURCE")
		for id := range allIDs {
			sp := storeMap[id]
			typ := "openai"
			baseURL := ""
			creds := "env"
			enabled := "yes"
			source := "runtime"
			modelCount := 0
			for _, m := range models {
				mm, _ := m.(map[string]any)
				if mm["provider_id"] == id {
					modelCount++
				}
			}
			if sp != nil {
				source = "store"
				if t, ok := sp["type"].(string); ok {
					typ = t
				}
				if u, ok := sp["base_url"].(string); ok {
					baseURL = u
				}
				if c, ok := sp["cred_store"].(string); ok && c != "" {
					creds = c
				}
				if sp["enabled"] == false {
					enabled = "no"
				}
			}
			if baseURL == "" {
				if ep, ok := adapterMap[id]; ok {
					baseURL = stripHealthSuffix(ep)
				}
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", id, typ, baseURL, creds, enabled, modelCount, source)
		}
		_ = tw.Flush()
		return
	}

	switch args[0] {
	case "add":
		requireArgs(args, 2, "provider add <json>")
		result := doPost("/admin/v1/providers", args[1])
		if result["ok"] == true {
			fmt.Println("Provider saved.")
		}
	case "edit":
		requireArgs(args, 3, "provider edit <id> <json>")
		result := doPatch("/admin/v1/providers/"+args[1], args[2])
		if result["ok"] == true {
			fmt.Println("Provider updated.")
		}
	case "delete":
		requireArgs(args, 2, "provider delete <id>")
		result := doDelete("/admin/v1/providers/" + args[1])
		if result["ok"] == true {
			fmt.Println("Provider deleted.")
		}
	case "discover":
		requireArgs(args, 2, "provider discover <id>")
		doDiscover(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown provider command: %s\n", args[0])
		os.Exit(1)
	}
}

func doModels(args []string) {
	if len(args) == 0 || args[0] == "list" {
		data := doGet("/admin/v1/engine/models")
		models, _ := data["models"].([]any)
		if len(models) == 0 {
			fmt.Println("No models registered.")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "MODEL\tPROVIDER\tWEIGHT\tCONTEXT\tIN $/1K\tOUT $/1K\tENABLED")
		for _, m := range models {
			mm, _ := m.(map[string]any)
			id, _ := mm["id"].(string)
			pid, _ := mm["provider_id"].(string)
			weight := fmtNum(mm["weight"])
			ctx := fmtNum(mm["max_context_tokens"])
			in := fmtCost(mm["input_per_1k"])
			out := fmtCost(mm["output_per_1k"])
			enabled := "yes"
			if mm["enabled"] == false {
				enabled = "no"
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, pid, weight, ctx, in, out, enabled)
		}
		_ = tw.Flush()
		return
	}

	switch args[0] {
	case "add":
		requireArgs(args, 2, "model add <json>")
		result := doPost("/admin/v1/models", args[1])
		if result["ok"] == true {
			fmt.Println("Model saved.")
		}
	case "edit":
		requireArgs(args, 3, "model edit <id> <json>")
		result := doPatch("/admin/v1/models/"+args[1], args[2])
		if result["ok"] == true {
			fmt.Println("Model updated.")
		}
	case "delete":
		requireArgs(args, 2, "model delete <id>")
		result := doDelete("/admin/v1/models/" + args[1])
		if result["ok"] == true {
			fmt.Println("Model deleted.")
		}
	case "enable":
		requireArgs(args, 2, "model enable <id>")
		result := doPatch("/admin/v1/models/"+args[1], `{"enabled":true}`)
		if result["ok"] == true {
			fmt.Printf("Model %s enabled.\n", args[1])
		}
	case "disable":
		requireArgs(args, 2, "model disable <id>")
		result := doPatch("/admin/v1/models/"+args[1], `{"enabled":false}`)
		if result["ok"] == true {
			fmt.Printf("Model %s disabled.\n", args[1])
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown model command: %s\n", args[0])
		os.Exit(1)
	}
}

func doRouting(args []string) {
	if len(args) == 0 || args[0] == "get" {
		data := doGet("/admin/v1/routing-config")
		fmt.Println(prettyJSON(data))
		return
	}
	switch args[0] {
	case "set":
		requireArgs(args, 2, "routing set <json>")
		result := doPut("/admin/v1/routing-config", args[1])
		if result["ok"] == true {
			fmt.Println("Routing config updated.")
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown routing command: %s\n", args[0])
		os.Exit(1)
	}
}

func doAPIKeys(args []string) {
	if len(args) == 0 || args[0] == "list" {
		data := doGet("/admin/v1/apikeys")
		keys, _ := data["keys"].([]any)
		if keys == nil {
			if items, ok := data["items"].([]any); ok {
				keys = items
			}
		}
		if len(keys) == 0 {
			fmt.Println("No API keys.")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tSCOPES\tENABLED\tCREATED\tLAST USED")
		for _, k := range keys {
			m, _ := k.(map[string]any)
			id, _ := m["id"].(string)
			name, _ := m["name"].(string)
			prefix, _ := m["prefix"].(string)
			scopes, _ := m["scopes"].(string)
			enabled := "yes"
			if m["enabled"] == false {
				enabled = "no"
			}
			created := fmtTime(m["created_at"])
			lastUsed := fmtTime(m["last_used_at"])
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, name, prefix, scopes, enabled, created, lastUsed)
		}
		_ = tw.Flush()
		return
	}

	switch args[0] {
	case "create":
		requireArgs(args, 2, "apikey create <json>")
		result := doPost("/admin/v1/apikeys", args[1])
		if result["ok"] == true {
			key, _ := result["key"].(string)
			id, _ := result["id"].(string)
			fmt.Printf("API key created.\n  ID:  %s\n  Key: %s\n", id, key)
			if w, ok := result["warning"].(string); ok && w != "" {
				fmt.Printf("  Warning: %s\n", w)
			}
			fmt.Println("\n  Save this key now — it will not be shown again.")
		}
	case "rotate":
		requireArgs(args, 2, "apikey rotate <id>")
		result := doPost("/admin/v1/apikeys/"+args[1]+"/rotate", "{}")
		if result["ok"] == true {
			key, _ := result["key"].(string)
			fmt.Printf("API key rotated.\n  New key: %s\n", key)
			fmt.Println("\n  Save this key now — it will not be shown again.")
		}
	case "edit":
		requireArgs(args, 3, "apikey edit <id> <json>")
		result := doPatch("/admin/v1/apikeys/"+args[1], args[2])
		if result["ok"] == true {
			fmt.Println("API key updated.")
		}
	case "delete":
		requireArgs(args, 2, "apikey delete <id>")
		result := doDelete("/admin/v1/apikeys/" + args[1])
		if result["ok"] == true {
			fmt.Println("API key deleted.")
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown apikey command: %s\n", args[0])
		os.Exit(1)
	}
}

func doLogs(args []string) {
	limit := parseLimit(args)
	data := doGet(fmt.Sprintf("/admin/v1/logs?limit=%d", limit))
	logs, _ := data["logs"].([]any)
	if len(logs) == 0 {
		fmt.Println("No request logs.")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tMODEL\tPROVIDER\tMODE\tLATENCY\tCOST\tSTATUS")
	for _, l := range logs {
		m, _ := l.(map[string]any)
		ts := fmtTime(m["timestamp"])
		model, _ := m["model_id"].(string)
		prov, _ := m["provider_id"].(string)
		mode, _ := m["mode"].(string)
		lat := fmtDuration(m["latency_ms"])
		cost := fmtCost(m["cost_usd"])
		status := fmtNum(m["status_code"])
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ts, model, prov, mode, lat, cost, status)
	}
	_ = tw.Flush()
}

func doAudit(args []string) {
	limit := parseLimit(args)
	data := doGet(fmt.Sprintf("/admin/v1/audit?limit=%d", limit))
	logs, _ := data["logs"].([]any)
	if len(logs) == 0 {
		fmt.Println("No audit logs.")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TIME\tACTION\tRESOURCE\tREQUEST ID")
	for _, l := range logs {
		m, _ := l.(map[string]any)
		ts := fmtTime(m["timestamp"])
		action, _ := m["action"].(string)
		resource, _ := m["resource"].(string)
		reqID, _ := m["request_id"].(string)
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ts, action, resource, reqID)
	}
	_ = tw.Flush()
}

func doRewards(args []string) {
	limit := parseLimit(args)
	data := doGet(fmt.Sprintf("/admin/v1/rewards?limit=%d", limit))
	rewards, _ := data["rewards"].([]any)
	if len(rewards) == 0 {
		fmt.Println("No reward entries.")
		return
	}
	fmt.Println(prettyJSON(rewards))
}

func doStats() {
	data := doGet("/admin/v1/stats")
	fmt.Println(prettyJSON(data))
}

func doEngine(args []string) {
	if len(args) == 0 || args[0] == "models" {
		data := doGet("/admin/v1/engine/models")
		models, _ := data["models"].([]any)
		adapterInfo, _ := data["adapter_info"].([]any)

		fmt.Printf("Adapters: %d\n", len(adapterInfo))
		for _, a := range adapterInfo {
			m, _ := a.(map[string]any)
			id, _ := m["id"].(string)
			ep, _ := m["health_endpoint"].(string)
			fmt.Printf("  %s → %s\n", id, ep)
		}
		fmt.Printf("\nModels: %d\n", len(models))
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  MODEL\tPROVIDER\tWEIGHT\tCONTEXT\tENABLED")
		for _, m := range models {
			mm, _ := m.(map[string]any)
			id, _ := mm["id"].(string)
			pid, _ := mm["provider_id"].(string)
			weight := fmtNum(mm["weight"])
			ctx := fmtNum(mm["max_context_tokens"])
			enabled := "yes"
			if mm["enabled"] == false {
				enabled = "no"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", id, pid, weight, ctx, enabled)
		}
		_ = tw.Flush()
		return
	}
	fmt.Fprintf(os.Stderr, "usage: tokenhubctl engine models\n")
	os.Exit(1)
}

func doEvents() {
	resp, err := doRequest("GET", "/admin/v1/events", nil)
	fatal(err)
	defer func() { _ = resp.Body.Close() }()

	fmt.Println("Streaming events (Ctrl-C to stop)...")
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			lines := strings.Split(string(buf[:n]), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "data:") {
					payload := strings.TrimPrefix(line, "data:")
					payload = strings.TrimSpace(payload)
					var evt map[string]any
					if json.Unmarshal([]byte(payload), &evt) == nil {
						evtType, _ := evt["type"].(string)
						model, _ := evt["model_id"].(string)
						provider, _ := evt["provider_id"].(string)
						latency := fmtDuration(evt["latency_ms"])
						reason, _ := evt["reason"].(string)
						errMsg, _ := evt["error"].(string)
						ts := time.Now().Format("15:04:05")
						if evtType == "route_error" {
							fmt.Printf("[%s] %s  model=%s provider=%s error=%s\n", ts, evtType, model, provider, errMsg)
						} else {
							fmt.Printf("[%s] %s  model=%s provider=%s latency=%s reason=%s\n", ts, evtType, model, provider, latency, reason)
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				fmt.Println("Event stream closed.")
			}
			break
		}
	}
}

func doDiscover(args []string) {
	requireArgs(args, 1, "discover <provider-id>")
	data := doGet("/admin/v1/providers/" + args[0] + "/discover")
	models, _ := data["models"].([]any)
	if len(models) == 0 {
		fmt.Println("No models discovered.")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MODEL ID\tREGISTERED")
	for _, m := range models {
		mm, _ := m.(map[string]any)
		id, _ := mm["id"].(string)
		registered := "no"
		if mm["registered"] == true {
			registered = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", id, registered)
	}
	_ = tw.Flush()
}

func doModelTest(args []string) {
	requireArgs(args, 1, "model-test <model-id> [api-key]")
	modelID := args[0]

	// Use provided key, then TOKENHUB_API_KEY env, then admin token as fallback.
	apiKey := ""
	if len(args) > 1 {
		apiKey = args[1]
	}
	if apiKey == "" {
		apiKey = os.Getenv("TOKENHUB_API_KEY")
	}
	if apiKey == "" {
		apiKey = adminToken()
	}

	payload := fmt.Sprintf(`{"model":%s,"messages":[{"role":"user","content":"Say the word OK and nothing else."}],"max_tokens":5}`, jsonStr(modelID))
	req, err := http.NewRequest("POST", baseURL()+"/v1/chat/completions", strings.NewReader(payload))
	fatal(err)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(start)
	fatal(err)
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Model:      %s\n", modelID)
	fmt.Printf("Status:     %d\n", resp.StatusCode)
	fmt.Printf("Latency:    %v\n", latency.Round(time.Millisecond))
	if resp.StatusCode == 200 {
		var out map[string]any
		if json.Unmarshal(body, &out) == nil {
			if choices, ok := out["choices"].([]any); ok && len(choices) > 0 {
				if ch, ok := choices[0].(map[string]any); ok {
					if msg, ok := ch["message"].(map[string]any); ok {
						content, _ := msg["content"].(string)
						reasoning, _ := msg["reasoning_content"].(string)
						if content != "" {
							fmt.Printf("Response:   %s\n", content)
						} else if reasoning != "" {
							// Reasoning model: show partial reasoning (model is thinking-only within budget)
							if len(reasoning) > 80 {
								reasoning = reasoning[:77] + "..."
							}
							fmt.Printf("Response:   [reasoning] %s\n", reasoning)
						} else {
							fmt.Printf("Response:   (empty)\n")
						}
					}
				}
			}
			if usage, ok := out["usage"].(map[string]any); ok {
				fmt.Printf("Tokens:     in=%v out=%v\n", usage["prompt_tokens"], usage["completion_tokens"])
			}
			if mdl, ok := out["model"].(string); ok {
				fmt.Printf("Model used: %s\n", mdl)
			}
		}
	} else {
		fmt.Printf("Error:      %s\n", string(body))
	}
}

func doProviderStatus(args []string) {
	requireArgs(args, 1, "provider-status <provider-id>")
	id := args[0]

	// Get health data and find the specific provider.
	data := doGet("/admin/v1/health")
	providers, _ := data["providers"].([]any)
	for _, p := range providers {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if m["provider_id"] == id {
			fmt.Printf("Provider:         %s\n", id)
			fmt.Printf("State:            %s\n", m["state"])
			fmt.Printf("Total requests:   %s\n", fmtNum(m["total_requests"]))
			fmt.Printf("Total errors:     %s\n", fmtNum(m["total_errors"]))
			fmt.Printf("Consec errors:    %s\n", fmtNum(m["consec_errors"]))
			fmt.Printf("Avg latency:      %s\n", fmtDuration(m["avg_latency_ms"]))
			fmt.Printf("Last success:     %s\n", fmtTime(m["last_success_at"]))
			if le, _ := m["last_error"].(string); le != "" {
				fmt.Printf("Last error:       %s\n", le)
				fmt.Printf("Last error at:    %s\n", fmtTime(m["last_error_time"]))
			}
			if cu, _ := m["cooldown_until"].(string); cu != "" && cu != "0001-01-01T00:00:00Z" {
				fmt.Printf("Cooldown until:   %s\n", fmtTime(m["cooldown_until"]))
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "provider %q not found in health data\n", id)
	os.Exit(1)
}

func doSimulate(args []string) {
	requireArgs(args, 1, "simulate <json>")
	result := doPost("/admin/v1/routing/simulate", args[0])
	fmt.Println(prettyJSON(result))
}

func doTSDB(args []string) {
	requireArgs(args, 1, "tsdb <query|metrics|prune> [args]")
	switch args[0] {
	case "metrics":
		data := doGet("/admin/v1/tsdb/metrics")
		fmt.Println(prettyJSON(data))
	case "prune":
		result := doPost("/admin/v1/tsdb/prune", "{}")
		fmt.Println(prettyJSON(result))
	case "query":
		qs := ""
		if len(args) > 1 {
			qs = "?" + strings.Join(args[1:], "&")
		}
		data := doGet("/admin/v1/tsdb/query" + qs)
		fmt.Println(prettyJSON(data))
	default:
		fmt.Fprintf(os.Stderr, "unknown tsdb command: %s\n", args[0])
		os.Exit(1)
	}
}

// --- Formatting helpers ---

func fmtNum(v any) string {
	if v == nil {
		return "-"
	}
	switch n := v.(type) {
	case float64:
		if n == float64(int(n)) {
			return strconv.Itoa(int(n))
		}
		return strconv.FormatFloat(n, 'f', 2, 64)
	case int:
		return strconv.Itoa(n)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func fmtCost(v any) string {
	if v == nil {
		return "-"
	}
	if f, ok := v.(float64); ok {
		if f == 0 {
			return "free"
		}
		return fmt.Sprintf("$%.4f", f)
	}
	return fmt.Sprintf("%v", v)
}

func fmtDuration(v any) string {
	if v == nil {
		return "-"
	}
	if f, ok := v.(float64); ok {
		if f < 1000 {
			return fmt.Sprintf("%.0fms", f)
		}
		return fmt.Sprintf("%.1fs", f/1000)
	}
	return fmt.Sprintf("%v", v)
}

func fmtTime(v any) string {
	if v == nil {
		return "-"
	}
	if s, ok := v.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Local().Format("2006-01-02 15:04:05")
		}
		return s
	}
	return fmt.Sprintf("%v", v)
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func stripHealthSuffix(endpoint string) string {
	for _, suffix := range []string{"/v1/models", "/v1/messages", "/health", "/v1"} {
		if strings.HasSuffix(endpoint, suffix) {
			return strings.TrimSuffix(endpoint, suffix)
		}
	}
	return endpoint
}

func init() {
	http.DefaultTransport.(*http.Transport).DisableKeepAlives = true
	http.DefaultClient.Timeout = 30 * time.Second
}
