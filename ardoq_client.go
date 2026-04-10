package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	naisTeamWorkspaceID  = "d64786fe39f101cde6329269" // Naisteam, og team
	naisTeamTypeID       = "p1775462930727"
	komponentWorkspaceID = "e08db3ca67512f4f66b4daeb" // Plattform, og komponent
	komponentTypeID      = "p1763722903209"
	databaseWorkspaceID  = "efcaf4ca19eef70f883e686f" // Miljø, instans, databaseprodukt, og database
	databaseTypeID       = "p1764106328620"

	databaseTypeBruksDB = "BruksDB"
)

// CustomFields holds the workspace-specific custom fields on a component.
type CustomFields struct {
	NaisTeam           string `json:"nais_team"`
	SistInnlestFraNais string `json:"sist_innlest_fra_nais,omitempty"`
	Namespace          string `json:"namespace,omitempty"`
	KomponentURL       string `json:"komponent_url,omitempty"`
	NaisConsoleLink    string `json:"nais_console_link,omitempty"`
	Slack              string `json:"slack,omitempty"`
	Medlemmer          int    `json:"medlemmer,omitempty"`
	AuditEnabled       bool   `json:"auditlogg_pa_db_er_etablert,omitempty"`
	DatabaseType       string `json:"database_type,omitempty"`
}

// BatchRequest is the top-level payload for POST /api/v2/batch.
type BatchRequest struct {
	Components BatchComponents `json:"components"`
}

// BatchComponents holds the upsert list for components in a batch request.
type BatchComponents struct {
	Upsert []BatchUpsert `json:"upsert,omitempty"`
}

// BatchUpsert is a single upsert operation within a batch request.
type BatchUpsert struct {
	BatchID  string        `json:"batchId,omitempty"`
	UniqueBy []string      `json:"uniqueBy"`
	Body     ComponentBody `json:"body"`
}

// ComponentBody is the component payload sent in a batch upsert.
type ComponentBody struct {
	Name          string       `json:"name"`
	Description   string       `json:"description,omitempty"`
	RootWorkspace string       `json:"rootWorkspace"`
	TypeID        string       `json:"typeId"`
	Parent        string       `json:"parent,omitempty"`
	CustomFields  CustomFields `json:"customFields"`
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

// batchSync sends upsert operations to the Batch API in chunks of 200.
func (c *ardoqClient) batchSync(upserts []BatchUpsert) error {
	slog.Info("should upsert", "len", len(upserts))
	payload, err := json.Marshal(upserts)
	if err != nil {
		return err
	}

	fmt.Println(string(payload))

	const chunkSize = 200
	for i := 0; i < len(upserts); i += chunkSize {
		end := min(i+chunkSize, len(upserts))
		chunk := upserts[i:end]

		req := BatchRequest{Components: BatchComponents{Upsert: chunk}}
		url := c.host + "/api/v2/batch"
		if _, err := c.do(http.MethodPost, url, req); err != nil {
			return fmt.Errorf("batch sync (offset %d): %w", i, err)
		}
		slog.Info("batch synced", "count", len(chunk))
	}
	return nil
}

// Plattform
// curl -H "Authorization: Bearer adq_" 'https://navit.ardoq.com/api/v2/components?typeId=p1765520821470'
func envToKomponentParent(env string) string {
	switch env {
	case "prod-gcp":
		return "eab26b68b4615fc1e4f4ab50"
		return "7746721acd82718fa558401b" // Nais GCP Prod
	case "dev-gcp":
		return "7746721acd82718fa558401b" // Nais GCP Dev
	case "prod-fss":
		return "7c6f23f3ae50f23af07f8008" // Nais FSS Prod
	case "dev-fss":
		return "d02abec8647e68cbe1b1f69e" // Nais FSS Dev
	default:
		return ""
	}
}

func envToPostgresParent(env string) string {
	switch env {
	case "prod-gcp":
		return "0e8c98f1aa2e2a4c49c9cb3a" // Postgres DB GCP Prod
	case "dev-gcp":
		return "231f2e0c2a97e281532842fb" // Postgres DB GCP Dev
	default:
		return ""
	}
}

// syncToArdoq upserts all workload components derived from Nais teams into Ardoq.
func syncToArdoq(teams map[string]Team, host, token string) error {
	c := newArdoqClient(host, token)

	var upserts []BatchUpsert
	for slug, team := range teams {
		upserts = append(upserts, BatchUpsert{
			BatchID:  slug,
			UniqueBy: []string{"name", "rootWorkspace"},
			Body: ComponentBody{
				Name:          slug,
				Description:   team.Purpose,
				RootWorkspace: naisTeamWorkspaceID,
				TypeID:        naisTeamTypeID,
				CustomFields: CustomFields{
					NaisTeam:        slug,
					NaisConsoleLink: "https://console.nav.cloud.nais.io/team/" + slug,
					Slack:           team.SlackChannel,
					Medlemmer:       team.Members,
				},
			},
		})

		for _, wl := range team.Applications {
			upserts = append(upserts, app(wl))
			upserts = append(upserts, postgres(wl)...)
			upserts = append(upserts, valkey(wl)...)
			upserts = append(upserts, openSearch(wl)...)
		}
	}

	sistInnlestFraNais := time.Now().UTC().Format("2006-01-02")
	for _, upsert := range upserts {
		upsert.Body.CustomFields.SistInnlestFraNais = sistInnlestFraNais
	}

	slog.Info("upserting workload components", "count", len(upserts))
	return c.batchSync(upserts)
}

func app(wl Workload) BatchUpsert {
	parent := envToKomponentParent(wl.Env)
	if parent == "" {
		slog.Info("missing environment", "env", wl.Env)
		return BatchUpsert{}
	}

	return BatchUpsert{
		BatchID:  wl.Team() + "/" + wl.Name,
		UniqueBy: []string{"name", "rootWorkspace"},
		Body: ComponentBody{
			Name:          wl.Name,
			RootWorkspace: komponentWorkspaceID,
			TypeID:        komponentTypeID,
			Parent:        parent,
			CustomFields: CustomFields{
				NaisTeam:     wl.Team(),
				KomponentURL: wl.IngressesAsString(),
			},
		},
	}
}

func postgres(wl Workload) []BatchUpsert {
	upserts := []BatchUpsert{}

	postgresParent := envToPostgresParent(wl.Env)
	if postgresParent == "" {
		slog.Info("missing Postgres parent", "env", wl.Env)
		return upserts
	}

	for _, database := range wl.Postgres {
		upserts = append(upserts, BatchUpsert{
			BatchID:  wl.Team() + "/postgres/" + wl.Name + "/" + database.Name,
			UniqueBy: []string{"name", "rootWorkspace"},
			Body: ComponentBody{
				Name:          database.Name,
				RootWorkspace: databaseWorkspaceID,
				TypeID:        databaseTypeID,
				Parent:        postgresParent,
				CustomFields: CustomFields{
					NaisTeam:     wl.Team(),
					AuditEnabled: database.Audit,
					DatabaseType: databaseTypeBruksDB,
				},
			},
		})
	}

	return upserts
}

func valkey(wl Workload) []BatchUpsert {
	upserts := []BatchUpsert{}
	for _, valkey := range wl.Valkey {
		upserts = append(upserts, BatchUpsert{
			BatchID:  wl.Team() + "/valkey/" + wl.Name + "/" + valkey.Name,
			UniqueBy: []string{"name", "rootWorkspace"},
			Body: ComponentBody{
				Name: valkey.Name,
				// RootWorkspace: databaseWorkspaceID,
				// TypeID:        databaseTypeID,
				// Parent:        postgresDBGCPParentID,
				CustomFields: CustomFields{
					NaisTeam: wl.Team(),
					// DatabaseType: databaseTypeBruksDB,
				},
			},
		})
	}

	return upserts
}

func openSearch(wl Workload) []BatchUpsert {
	upserts := []BatchUpsert{}
	for _, openSearch := range wl.OpenSearch {
		upserts = append(upserts, BatchUpsert{
			BatchID:  wl.Team() + "/openSearch/" + wl.Name + "/" + openSearch.Name,
			UniqueBy: []string{"name", "rootWorkspace"},
			Body: ComponentBody{
				Name: openSearch.Name,
				// RootWorkspace: databaseWorkspaceID,
				// TypeID:        databaseTypeID,
				// Parent:        postgresDBGCPParentID,
				CustomFields: CustomFields{
					NaisTeam: wl.Team(),
					// DatabaseType: databaseTypeBruksDB,
				},
			},
		})
	}

	return upserts
}
