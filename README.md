# Nais Ardoq sync — Nais Console → Ardoq

Fetches team and workload data from the [Nais Console](https://console.nav.cloud.nais.io/) GraphQL API and syncs it into [Ardoq](https://navit.ardoq.com/).

## Setup

Install [Mise](https://mise.jdx.dev/) if you haven't already, then let it install the correct Go version:

```bash
mise install
```

Build the binary:

```bash
go build -o ardoq .
```

## Configuration

Set the following environment variables:

| Variable           | Required | Default                         | Description                   |
|--------------------|----------|---------------------------------|-------------------------------|
| `NAIS_CONSOLE_URL` | No       | `http://localhost:4242/graphql` | Nais Console GraphQL endpoint |
| `ARDOQ_API_TOKEN`  | Yes      | —                               | Ardoq API token               |
| `ARDOQ_HOST`       | No       | `https://navit.ardoq.com`       | Ardoq instance URL            |

> **Note:** If `ARDOQ_API_TOKEN` is not set, the app will still fetch from Nais Console but skip the Ardoq sync.

## Usage

Port-forward the Nais Console API (if needed):

```bash
nais auth login -n
nais alpha api proxy
```

Run the sync:

```bash
export ARDOQ_API_TOKEN="your-token-here"
./ardoq
```

## Deployment

Pushing to `main` automatically builds the Docker image and deploys to the `prod-gcp` cluster via GitHub Actions. 
The workflow uses `nais/docker-build-push` and `nais/deploy` — no manual steps required.

## Ardoq Data Model

The app creates components in Ardoq with this hierarchy:

- **Team** (top-level component) — one per Nais team slug
  - **Workload** (child component) — named `app-name (environment)`
    - Fields: `environment`, `sqlinstances` (if present)

The sync is idempotent — running it multiple times will not create duplicates.
