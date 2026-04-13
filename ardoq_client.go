package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	importConfigIDComponents = "0ed5625c6f6ce16b9b9417ba"
	importConfigIDReferences = "ebdf8cf9a7bffd5fbce24747"
	importEndpoint           = "/api/integrations/tabular/import"
)

// ImportConfig identifies the pre-defined Ardoq import configuration to use.
type ImportConfig struct {
	ID string `json:"id"`
}

// ImportTable is a single worksheet in an import payload.
type ImportTable struct {
	ID   string              `json:"id"`
	Rows []map[string]string `json:"rows"`
}

// ImportPayload is the top-level request body for POST /api/integrations/tabular/import.
type ImportPayload struct {
	Config ImportConfig  `json:"config"`
	Tables []ImportTable `json:"tables"`
}

type ardoqClient struct {
	host   string
	token  string
	client *http.Client
}

func newArdoqClient(host, token string) *ardoqClient {
	return &ardoqClient{
		host:   strings.TrimRight(host, "/"),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *ardoqClient) do(method, url string, body any) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token token="+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, url, respBody)
	}
	return respBody, nil
}

// dryRunToArdoq builds the Import API payloads and writes them to disk as
// ardoq-components.json and ardoq-references.json without calling Ardoq.
func dryRunToArdoq(teams map[string]Team) error {
	table1 := buildTeamsTable(teams)
	table2, table3 := buildWorkloadTables(teams)
	table4, table5 := buildReferenceTables(teams)

	componentPayload := ImportPayload{
		Config: ImportConfig{ID: importConfigIDComponents},
		Tables: []ImportTable{table1, table2, table3},
	}
	referencePayload := ImportPayload{
		Config: ImportConfig{ID: importConfigIDReferences},
		Tables: []ImportTable{table4, table5},
	}

	if err := writeJSON("ardoq-components.json", componentPayload); err != nil {
		return err
	}
	return writeJSON("ardoq-references.json", referencePayload)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// syncToArdoq sends all Nais team data to Ardoq via the Import API in two steps:
// Step 1 — components (teams, app instances, database instances)
// Step 2 — references (team→app, app→database)
func syncToArdoq(teams map[string]Team, host, token string) error {
	c := newArdoqClient(host, token)

	table1 := buildTeamsTable(teams)
	table2, table3 := buildWorkloadTables(teams)
	table4, table5 := buildReferenceTables(teams)

	slog.Info("importing components", "teams", len(table1.Rows), "apps", len(table2.Rows), "databases", len(table3.Rows))
	componentPayload := ImportPayload{
		Config: ImportConfig{ID: importConfigIDComponents},
		Tables: []ImportTable{table1, table2, table3},
	}
	url := c.host + importEndpoint
	if _, err := c.do(http.MethodPost, url, componentPayload); err != nil {
		return fmt.Errorf("import components: %w", err)
	}
	slog.Info("components imported")

	slog.Info("importing references", "team_app_refs", len(table4.Rows), "app_db_refs", len(table5.Rows))
	referencePayload := ImportPayload{
		Config: ImportConfig{ID: importConfigIDReferences},
		Tables: []ImportTable{table4, table5},
	}
	if _, err := c.do(http.MethodPost, url, referencePayload); err != nil {
		return fmt.Errorf("import references: %w", err)
	}
	slog.Info("references imported")

	return nil
}

// buildTeamsTable builds table 1: one row per team.
func buildTeamsTable(teams map[string]Team) ImportTable {
	rows := make([]map[string]string, 0, len(teams))
	for slug, team := range teams {
		rows = append(rows, map[string]string{
			"navn":        slug,
			"beskrivelse": team.Purpose,
			"kanal":       team.SlackChannel,
		})
	}
	return ImportTable{ID: "1", Rows: rows}
}

// buildWorkloadTables builds table 2 (app instances) and table 3 (database instances).
func buildWorkloadTables(teams map[string]Team) (ImportTable, ImportTable) {
	var appRows []map[string]string
	var dbRows []map[string]string

	for _, team := range teams {
		for _, wl := range team.Applications {
			env := naisEnvToArdoq(wl.Env)
			if env == "" {
				slog.Warn("unknown environment, skipping workload", "env", wl.Env, "app", wl.Name)
				continue
			}

			appRows = append(appRows, map[string]string{
				"miljø":     envSuffix(env),
				"navn":      wl.Name,
				"ingresser": wl.IngressesAsString(),
			})

			for _, db := range wl.Postgres {
				dbEnv := naisEnvToArdoqPostgres(wl.Env)
				if dbEnv == "" {
					slog.Warn("unsupported environment for Postgres, skipping", "env", wl.Env, "db", db.Name)
					continue
				}
				dbRows = append(dbRows, map[string]string{
					"miljø":     envSuffix(dbEnv),
					"navn":      db.Name,
					"auditlogg": boolToString(db.Audit),
				})
			}

			for _, v := range wl.Valkey {
				vEnv := naisEnvToArdoqValkey(wl.Env)
				if vEnv == "" {
					slog.Warn("unsupported environment for Valkey, skipping", "env", wl.Env, "valkey", v.Name)
					continue
				}
				dbRows = append(dbRows, map[string]string{
					"miljø": envSuffix(vEnv),
					"navn":  v.Name,
				})
			}

			o := wl.OpenSearch
			if o != nil {
				oEnv := naisEnvToArdoqOpenSearch(wl.Env)
				if oEnv == "" {
					slog.Warn("unsupported environment for OpenSearch, skipping", "env", wl.Env, "opensearch", o.Name)
					continue
				}
				dbRows = append(dbRows, map[string]string{
					"miljø": envSuffix(oEnv),
					"navn":  o.Name,
				})
			}
		}
	}

	return ImportTable{ID: "2", Rows: appRows}, ImportTable{ID: "3", Rows: dbRows}
}

// buildReferenceTables builds table 4 (team→app refs) and table 5 (app→database refs).
// References use only the prefix before '::' in the environment name.
func buildReferenceTables(teams map[string]Team) (ImportTable, ImportTable) {
	var teamAppRows []map[string]string
	var appDbRows []map[string]string

	for _, team := range teams {
		for _, wl := range team.Applications {
			env := naisEnvToArdoq(wl.Env)
			if env == "" {
				continue
			}

			teamAppRows = append(teamAppRows, map[string]string{
				"team":      team.Slug,
				"app_miljø": env,
				"app_navn":  wl.Name,
			})

			for _, db := range wl.Postgres {
				dbEnv := naisEnvToArdoqPostgres(wl.Env)
				if dbEnv == "" {
					continue
				}
				appDbRows = append(appDbRows, map[string]string{
					"app_miljø":      env,
					"app":            wl.Name,
					"database_miljø": dbEnv,
					"database":       db.Name,
				})
			}

			for _, v := range wl.Valkey {
				vEnv := naisEnvToArdoqValkey(wl.Env)
				if vEnv == "" {
					continue
				}
				appDbRows = append(appDbRows, map[string]string{
					"app_miljø":      env,
					"app":            wl.Name,
					"database_miljø": vEnv,
					"database":       v.Name,
				})
			}

			o := wl.OpenSearch
			if o != nil {
				oEnv := naisEnvToArdoqOpenSearch(wl.Env)
				if oEnv == "" {
					continue
				}
				appDbRows = append(appDbRows, map[string]string{
					"app_miljø":      env,
					"app":            wl.Name,
					"database_miljø": oEnv,
					"database":       o.Name,
				})
			}
		}
	}

	return ImportTable{ID: "4", Rows: teamAppRows}, ImportTable{ID: "5", Rows: appDbRows}
}

func boolToString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// envSuffix returns the part of an Ardoq environment name after '::'.
// For example, "Nais GCP::Nais GCP Prod" → "Nais GCP Prod".
func envSuffix(env string) string {
	if _, after, ok := strings.Cut(env, "::"); ok {
		return after
	}
	return env
}

// naisEnvToArdoq maps a Nais environment name to its Ardoq display name for apps.
// Returns an empty string for unknown environments.
func naisEnvToArdoq(env string) string {
	switch env {
	case "prod-gcp":
		return "Nais GCP::Nais GCP Prod"
	case "dev-gcp":
		return "Nais GCP::Nais GCP Dev"
	case "prod-fss":
		return "Nais FSS::Nais FSS Prod"
	case "dev-fss":
		return "Nais FSS::Nais FSS Dev"
	default:
		return ""
	}
}

// naisEnvToArdoqPostgres maps a Nais environment name to its Ardoq display name for Postgres databases.
// Returns an empty string for unknown or unsupported environments.
func naisEnvToArdoqPostgres(env string) string {
	switch env {
	case "prod-gcp":
		return "Postgres DB GCP::Postgres DB GCP Prod"
	case "dev-gcp":
		return "Postgres DB GCP::Postgres DB GCP Dev"
	default:
		return ""
	}
}

// naisEnvToArdoqValkey maps a Nais environment name to its Ardoq display name for Valkey instances.
// Returns an empty string for unknown or unsupported environments.
func naisEnvToArdoqValkey(env string) string {
	switch env {
	case "prod-gcp":
		return "Valkey GCP::Valkey GCP Prod"
	case "dev-gcp":
		return "Valkey GCP::Valkey GCP Dev"
	default:
		return ""
	}
}

// naisEnvToArdoqOpenSearch maps a Nais environment name to its Ardoq display name for OpenSearch instances.
// Returns an empty string for unknown or unsupported environments.
func naisEnvToArdoqOpenSearch(env string) string {
	switch env {
	case "prod-gcp":
		return "OpenSearch GCP::OpenSearch GCP Prod"
	case "dev-gcp":
		return "OpenSearch GCP::OpenSearch GCP Dev"
	default:
		return ""
	}
}
