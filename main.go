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
	teams, err := fetchTeamsFromNaisAPI(naisURL)
	if err != nil {
		slog.Error("failed to fetch teams", "error", err)
		os.Exit(1)
	}
	slog.Info("fetched teams", "count", len(teams))

	slog.Info("syncing to Ardoq")
	if err := toArdoq(teams, ardoqHost, ardoqToken, *dryRun); err != nil {
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
