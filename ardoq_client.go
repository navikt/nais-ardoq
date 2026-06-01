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
	importConfigIDComponents = "26272cdbc938f7b83e7a189d"
	importConfigIDReferences = "4296c56681dc7ec8067b134e"
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

// toArdoq builds the Import API payloads and either writes them to disk (dry-run)
// or sends them to Ardoq via the Import API.
func toArdoq(teams map[string]Team, host, token string, dryRun bool) error {
	table1 := buildTeamsTable(teams)
	table2, table3 := buildWorkloadTables(teams)
	table4, table5, table6, table7, table8 := buildReferenceTables(teams)

	componentPayload := ImportPayload{
		Config: ImportConfig{ID: importConfigIDComponents},
		Tables: []ImportTable{table1, table2, table3},
	}

	referencePayload := ImportPayload{
		Config: ImportConfig{ID: importConfigIDReferences},
		Tables: []ImportTable{table4, table5, table6, table7, table8},
	}

	if dryRun {
		slog.Info("dry-run mode — writing payloads to disk")
		if err := writeJSON("ardoq-components.json", componentPayload); err != nil {
			return err
		}
		if err := writeJSON("ardoq-references.json", referencePayload); err != nil {
			return err
		}
		slog.Info("dry-run complete")
		return nil
	}

	c := newArdoqClient(host, token)
	url := c.host + importEndpoint

	slog.Info("importing components", "teams", len(table1.Rows), "apps", len(table2.Rows), "databases", len(table3.Rows))
	if _, err := c.do(http.MethodPost, url, componentPayload); err != nil {
		return fmt.Errorf("import components: %w", err)
	}
	slog.Info("components imported")

	slog.Info("importing references", "team_app_refs", len(table4.Rows), "app_db_refs", len(table5.Rows))
	if _, err := c.do(http.MethodPost, url, referencePayload); err != nil {
		return fmt.Errorf("import references: %w", err)
	}
	slog.Info("references imported")

	return nil
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
	appSeen := make(map[string]map[string]string)
	dbSeen := make(map[string]map[string]string)

	for _, team := range teams {
		for _, wl := range team.Applications {
			env := naisEnvToCluster(wl.Env)
			if env == "" {
				slog.Warn("unknown environment, skipping workload", "env", wl.Env, "app", wl.Name)
				continue
			}

			appKey := fmt.Sprintf("nais:%s:%s:%s", env, team.Slug, wl.Name)
			if _, seen := appSeen[appKey]; !seen {
				appSeen[appKey] = map[string]string{
					"app_key":   appKey,
					"navn":      wl.Name,
					"ingresser": wl.IngressesAsString(),
				}
			}

			for _, db := range wl.Postgres {
				dbKey := fmt.Sprintf("db:%s:%s:postgres:%s", env, team.Slug, db.Name)
				if _, seen := dbSeen[dbKey]; !seen {
					dbSeen[dbKey] = map[string]string{
						"db_key":    dbKey,
						"navn":      db.Name,
						"auditlogg": boolToString(db.Audit),
					}
				}
			}

			for _, v := range wl.Valkey {
				dbKey := fmt.Sprintf("db:%s:%s:valkey:%s", env, team.Slug, v.Name)
				if _, seen := dbSeen[dbKey]; !seen {
					dbSeen[dbKey] = map[string]string{
						"db_key": dbKey,
						"navn":   v.Name,
					}
				}
			}

			o := wl.OpenSearch
			if o != nil {
				dbKey := fmt.Sprintf("db:%s:%s:opensearch:%s", env, team.Slug, o.Name)
				if _, seen := dbSeen[dbKey]; !seen {
					dbSeen[dbKey] = map[string]string{
						"db_key": dbKey,
						"navn":   o.Name,
					}
				}
			}
		}
	}

	return ImportTable{ID: "2", Rows: flattenRows(appSeen)},
		ImportTable{ID: "3", Rows: flattenRows(dbSeen)}
}

// buildReferenceTables builds reference tables 4–8:
// 4: team→app, 5: app→database, 6: app→environment, 7: database→environment, 8: database→technology
func buildReferenceTables(teams map[string]Team) (ImportTable, ImportTable, ImportTable, ImportTable, ImportTable) {
	teamAppSeen := make(map[string]map[string]string)
	appDbSeen := make(map[string]map[string]string)
	appEnvSeen := make(map[string]map[string]string)
	dbEnvSeen := make(map[string]map[string]string)
	dbTechSeen := make(map[string]map[string]string)

	for _, team := range teams {
		for _, wl := range team.Applications {
			if naisEnvToCluster(wl.Env) == "" {
				continue
			}

			plattformInstans := naisEnvToArdoq(wl.Env)
			if plattformInstans == "" {
				continue
			}

			appKey := fmt.Sprintf("nais:%s:%s:%s", naisEnvToCluster(wl.Env), team.Slug, wl.Name)

			teamAppKey := team.Slug + "|" + appKey
			if _, seen := teamAppSeen[teamAppKey]; !seen {
				teamAppSeen[teamAppKey] = map[string]string{
					"team":    team.Slug,
					"app_key": appKey,
				}
			}

			if _, seen := appEnvSeen[appKey]; !seen {
				appEnvSeen[appKey] = map[string]string{
					"app_key":   appKey,
					"plattform": plattformInstans,
				}
			}

			for _, db := range wl.Postgres {
				dbKey := fmt.Sprintf("db:%s:%s:postgres:%s", naisEnvToCluster(wl.Env), team.Slug, db.Name)
				appDbKey := appKey + "|" + dbKey
				if _, seen := appDbSeen[appDbKey]; !seen {
					appDbSeen[appDbKey] = map[string]string{
						"app_key": appKey,
						"db_key":  dbKey,
					}
				}
				if _, seen := dbEnvSeen[dbKey]; !seen {
					dbEnvSeen[dbKey] = map[string]string{
						"db_key":    dbKey,
						"plattform": plattformInstans,
					}
				}
				if _, seen := dbTechSeen[dbKey]; !seen {
					dbTechSeen[dbKey] = map[string]string{
						"db_key":            dbKey,
						"databaseteknologi": "postgres",
					}
				}
			}

			for _, v := range wl.Valkey {
				dbKey := fmt.Sprintf("db:%s:%s:valkey:%s", naisEnvToCluster(wl.Env), team.Slug, v.Name)
				appDbKey := appKey + "|" + dbKey
				if _, seen := appDbSeen[appDbKey]; !seen {
					appDbSeen[appDbKey] = map[string]string{
						"app_key": appKey,
						"db_key":  dbKey,
					}
				}
				if _, seen := dbEnvSeen[dbKey]; !seen {
					dbEnvSeen[dbKey] = map[string]string{
						"db_key":    dbKey,
						"plattform": plattformInstans,
					}
				}
				if _, seen := dbTechSeen[dbKey]; !seen {
					dbTechSeen[dbKey] = map[string]string{
						"db_key":            dbKey,
						"databaseteknologi": "valkey",
					}
				}
			}

			o := wl.OpenSearch
			if o != nil {
				dbKey := fmt.Sprintf("db:%s:%s:opensearch:%s", naisEnvToCluster(wl.Env), team.Slug, o.Name)
				appDbKey := appKey + "|" + dbKey
				if _, seen := appDbSeen[appDbKey]; !seen {
					appDbSeen[appDbKey] = map[string]string{
						"app_key": appKey,
						"db_key":  dbKey,
					}
				}
				if _, seen := dbEnvSeen[dbKey]; !seen {
					dbEnvSeen[dbKey] = map[string]string{
						"db_key":    dbKey,
						"plattform": plattformInstans,
					}
				}
				if _, seen := dbTechSeen[dbKey]; !seen {
					dbTechSeen[dbKey] = map[string]string{
						"db_key":            dbKey,
						"databaseteknologi": "opensearch",
					}
				}
			}
		}
	}

	return ImportTable{ID: "4", Rows: flattenRows(teamAppSeen)},
		ImportTable{ID: "5", Rows: flattenRows(appDbSeen)},
		ImportTable{ID: "6", Rows: flattenRows(appEnvSeen)},
		ImportTable{ID: "7", Rows: flattenRows(dbEnvSeen)},
		ImportTable{ID: "8", Rows: flattenRows(dbTechSeen)}
}

// flattenRows converts a deduplication map (keyed by some unique key) to a slice of rows.
func flattenRows(m map[string]map[string]string) []map[string]string {
	rows := make([]map[string]string, 0, len(m))
	for _, v := range m {
		rows = append(rows, v)
	}
	return rows
}

func boolToString(b bool) string {
	if b {
		return "ja"
	}
	return "nei"
}

// naisEnvToCluster maps a Nais environment name to its cluster identifier used in Ardoq keys.
// Returns an empty string for unknown environments.
func naisEnvToCluster(env string) string {
	switch env {
	case "prod-gcp":
		return "nais-gcp-prod"
	case "dev-gcp":
		return "nais-gcp-dev"
	case "prod-fss":
		return "nais-fss-prod"
	case "dev-fss":
		return "nais-fss-dev"
	default:
		return ""
	}
}

// naisEnvToArdoq maps a Nais environment name to its Ardoq display name for apps.
// Returns an empty string for unknown environments.
func naisEnvToArdoq(env string) string {
	switch env {
	case "prod-gcp":
		return "Nais GCP Prod"
	case "dev-gcp":
		return "Nais GCP Dev"
	case "prod-fss":
		return "Nais FSS Prod"
	case "dev-fss":
		return "Nais FSS Dev"
	default:
		return ""
	}
}
