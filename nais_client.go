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

type Postgres struct {
	Name  string
	Audit bool
}

type Valkey struct {
	Name string
}

type OpenSearch struct {
	Name string
}

// Workload represents a single Nais app workload.
type Workload struct { // TODO: Dette er en applications, så ingen støtte for naisjobs
	Name       string
	Env        string
	Ingresses  []string
	Postgres   []Postgres
	Valkey     []Valkey
	OpenSearch *OpenSearch
}

func (w Workload) IngressesAsString() string {
	return strings.Join(w.Ingresses, ", ")
}

type Team struct {
	Slug         string
	Purpose      string
	SlackChannel string
	Members      int
	Applications []Workload
}

const teamsQuery = `
query Teams($after: Cursor) {
  teams(filter: { hasWorkloads: true }, after: $after) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      slug
      purpose
      slackChannel
      members {
        pageInfo {
          totalCount
        }
      }
      applications(first: 500) {
        nodes {
          name
          ingresses {
            url
          }
          teamEnvironment {
            environment {
              name
            }
          }
          postgresInstances {
            nodes {
              name
              audit {
                enabled
              }
            }
          }
          sqlInstances {
            nodes {
              name
            }
          }
          valkeys {
            nodes {
              name
            }
          }
        }
      }
    }
  }
}
`

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type teamsResponse struct {
	Data struct {
		// midlertidig
		Team struct {
			Slug         string `json:"slug"`
			Purpose      string `json:"purpose"`
			SlackChannel string `json:"slackChannel"`
			Members      struct {
				PageInfo struct {
					Count int `json:"totalCount"`
				} `json:"pageInfo"`
			} `json:"members"`
			Applications struct {
				Nodes []struct {
					Name      string `json:"name"`
					Ingresses []struct {
						URL string `json:"url"`
					} `json:"ingresses"`
					TeamEnvironment struct {
						Environment struct {
							Name string `json:"name"`
						} `json:"environment"`
					} `json:"teamEnvironment"`
					PostgresInstances struct {
						Nodes []struct {
							Name  string `json:"name"`
							Audit struct {
								Enabled bool `json:"enabled"`
							} `json:"audit"`
						} `json:"nodes"`
					} `json:"postgresInstances"`
					SQLInstances struct {
						Nodes []struct {
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"sqlInstances"`
					ValkeyInstances struct {
						Nodes []struct {
							Name string `json:"name"`
						} `json:"nodes"`
					} `json:"valkeys"`
					OpenSearchInstances struct {
						Name string `json:"name"`
					} `json:"openSearch"`
				} `json:"nodes"`
			} `json:"applications"`
		} `json:"team"`
		// midlertidig
		Teams struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []struct {
				Slug         string `json:"slug"`
				Purpose      string `json:"purpose"`
				SlackChannel string `json:"slackChannel"`
				Members      struct {
					PageInfo struct {
						Count int `json:"totalCount"`
					} `json:"pageInfo"`
				} `json:"members"`
				Applications struct {
					Nodes []struct {
						Name      string `json:"name"`
						Ingresses []struct {
							URL string `json:"url"`
						} `json:"ingresses"`
						TeamEnvironment struct {
							Environment struct {
								Name string `json:"name"`
							} `json:"environment"`
						} `json:"teamEnvironment"`
						PostgresInstances struct {
							Nodes []struct {
								Name  string `json:"name"`
								Audit struct {
									Enabled bool `json:"enabled"`
								} `json:"audit"`
							} `json:"nodes"`
						} `json:"postgresInstances"`
						SQLInstances struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"sqlInstances"`
						ValkeyInstances struct {
							Nodes []struct {
								Name string `json:"name"`
							} `json:"nodes"`
						} `json:"valkeys"`
						OpenSearchInstances struct {
							Name string `json:"name"`
						} `json:"openSearch"`
					} `json:"nodes"`
				} `json:"applications"`
			} `json:"nodes"`
		} `json:"teams"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func fetchTeamsFromNaisAPI(consoleURL string) (map[string]Team, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	teams := make(map[string]Team)
	after := ""

	for {
		body, err := json.Marshal(gqlRequest{
			Query:     teamsQuery,
			Variables: map[string]any{"after": after},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequest(http.MethodPost, consoleURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		if !strings.Contains(consoleURL, "localhost") {
			token, err := os.ReadFile(os.Getenv("NAIS_SERVICE_ACCOUNT_TOKEN_PATH"))
			if err != nil {
				return nil, err
			}

			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("execute request: %w", err)
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody)
		}

		var result teamsResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		if len(result.Errors) > 0 {
			msgs := make([]string, len(result.Errors))
			for i, e := range result.Errors {
				msgs[i] = e.Message
			}

			slog.Warn(fmt.Sprintf("GraphQL errors (after: %s): %s", after, strings.Join(msgs, "; ")))
		}

		for _, teamNode := range result.Data.Teams.Nodes {
			team := teams[teamNode.Slug]
			if team.Slug == "" {
				team = Team{
					Slug:         teamNode.Slug,
					Purpose:      teamNode.Purpose,
					SlackChannel: teamNode.SlackChannel,
					Members:      teamNode.Members.PageInfo.Count,
					Applications: []Workload{},
				}
			}

			for _, wlNode := range teamNode.Applications.Nodes {
				postgres := make([]Postgres, 0, len(wlNode.SQLInstances.Nodes)+len(wlNode.PostgresInstances.Nodes))
				for _, inst := range wlNode.SQLInstances.Nodes {
					postgres = append(postgres, Postgres{
						Name:  inst.Name,
						Audit: false,
					})
				}

				for _, inst := range wlNode.PostgresInstances.Nodes {
					postgres = append(postgres, Postgres{
						Name:  inst.Name,
						Audit: inst.Audit.Enabled,
					})
				}

				valkey := make([]Valkey, 0, len(wlNode.ValkeyInstances.Nodes))
				for _, inst := range wlNode.ValkeyInstances.Nodes {
					valkey = append(valkey, Valkey{Name: inst.Name})
				}

				var openSearch *OpenSearch
				if wlNode.OpenSearchInstances.Name != "" {
					openSearch = &OpenSearch{
						Name: wlNode.OpenSearchInstances.Name,
					}
				}

				ingresses := []string{}
				for _, ingress := range wlNode.Ingresses {
					ingresses = append(ingresses, ingress.URL)
				}

				team.Applications = append(team.Applications, Workload{
					Name:       wlNode.Name,
					Env:        wlNode.TeamEnvironment.Environment.Name,
					Ingresses:  ingresses,
					Postgres:   postgres,
					Valkey:     valkey,
					OpenSearch: openSearch,
				})
			}

			teams[team.Slug] = team
		}

		if !result.Data.Teams.PageInfo.HasNextPage {
			break
		}
		after = result.Data.Teams.PageInfo.EndCursor
	}

	return teams, nil
}
