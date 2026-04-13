package main

import (
	"flag"
	"log/slog"
	"os"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "fetch from Nais and write Ardoq import payloads to disk instead of calling Ardoq")
	flag.Parse()

	naisURL := getenv("NAIS_CONSOLE_URL", "http://localhost:4242/graphql")
	ardoqHost := getenv("ARDOQ_HOST", "https://navit.ardoq.com")
	ardoqToken := os.Getenv("ARDOQ_API_TOKEN")

	slog.Info("fetching teams from Nais Console", "url", naisURL)
	teams, err := fetchTeams(naisURL)
	if err != nil {
		slog.Error("failed to fetch teams", "error", err)
		os.Exit(1)
	}
	slog.Info("fetched teams", "count", len(teams))

	if *dryRun {
		slog.Info("dry-run mode — writing payloads to disk")
		if err := dryRunToArdoq(teams); err != nil {
			slog.Error("dry-run failed", "error", err)
			os.Exit(1)
		}
		slog.Info("dry-run complete", "files", []string{"ardoq-components.json", "ardoq-references.json"})
		return
	}

	if ardoqToken == "" {
		slog.Warn("ARDOQ_API_TOKEN not set — skipping Ardoq sync")
		return
	}

	slog.Info("syncing to Ardoq")
	if err := syncToArdoq(teams, ardoqHost, ardoqToken); err != nil {
		slog.Error("Ardoq sync failed", "error", err)
		os.Exit(1)
	}
	slog.Info("sync complete")
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
