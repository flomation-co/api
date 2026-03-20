# Flomation API

Backend API server for the Flomation Automate platform, providing RESTful endpoints for managing automation workflows, executions, runners, and environments.

## Overview

The Flomation API is a Go service built with [Gin](https://github.com/gin-gonic/gin) that serves as the backend for the Flomation Automate platform. It handles workflow (Flo) management, execution orchestration, runner coordination, and environment/secret management. It connects to a PostgreSQL database and uses JWT-based authentication via an external identity service.

## Prerequisites

- Go 1.26.1+
- PostgreSQL database
- Access to the Flomation identity service (for authentication)
- `golangci-lint`, `goimports`, `gosec`, and `govulncheck` (for linting)

## Installation

```bash
git clone <repository-url>
cd api
go mod download
```

## Configuration

Configuration is loaded from `config.json` (and can be overridden via environment variables or CLI arguments).

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `LISTEN_ADDRESS` | HTTP listen address | No | — |
| `LISTEN_PORT` | HTTP listen port | No | — |
| `DATABASE_HOSTNAME` | PostgreSQL host | Yes | — |
| `DATABASE_PORT` | PostgreSQL port | Yes | — |
| `DATABASE_USER` | Database username | Yes | — |
| `DATABASE_PASSWORD` | Database password | Yes | — |
| `DATABASE_NAME` | Database name | Yes | — |
| `DATABASE_ENCRYPTION_KEY` | Key for encrypting sensitive data | Yes | — |
| `DATABASE_MAX_IDLE_CONNS` | Max idle database connections | No | — |
| `DATABASE_MAX_OPEN_CONNS` | Max open database connections | No | — |
| `DATABASE_SSL_MODE` | PostgreSQL SSL mode | No | — |
| `IDENTITY_SERVICE` | URL of the Flomation identity service | Yes | — |

Example `config.json`:

```json
{
  "http": {
    "address": "0.0.0.0",
    "port": 8888
  },
  "database": {
    "hostname": "localhost",
    "port": 5432,
    "username": "flomation",
    "password": "secret",
    "database": "flomation",
    "encryption_key": "your-encryption-key",
    "ssl_mode": "disable"
  },
  "security": {
    "identity_service": "https://identity.flomation.co"
  }
}
```

## Usage

### Running the server

```bash
go run ./cmd
```

The server starts on the configured address and port (default `:8888`) and automatically runs any pending database migrations on startup.

### API endpoints

All endpoints are under `/api/v1` unless noted. Most require a Bearer JWT token via the `Authorization` header.

| Group | Method | Path | Auth | Description |
|-------|--------|------|------|-------------|
| Version | GET | `/version` | No | Build version info |
| Dashboard | GET | `/api/v1/dashboard` | Yes | User dashboard data |
| Organisation | GET | `/api/v1/organisation` | Yes | List user's organisations |
| Organisation | GET | `/api/v1/organisation/:ID` | Yes | Get organisation by ID |
| Organisation | POST | `/api/v1/organisation` | Yes | Create organisation |
| Organisation | POST | `/api/v1/organisation/:ID` | Yes | Update organisation |
| User | GET | `/api/v1/user` | Yes | Get current user |
| User | GET | `/api/v1/user/:ID` | Yes | Get user by ID |
| User | POST | `/api/v1/user` | Yes | Create user |
| User | POST | `/api/v1/user/:ID` | Yes | Update user |
| Action | GET | `/api/v1/action` | No | List available actions |
| Flo | GET | `/api/v1/flo` | Yes | List user's flos |
| Flo | GET | `/api/v1/flo/:FloID` | Yes | Get flo by ID |
| Flo | POST | `/api/v1/flo` | Yes | Create flo |
| Flo | POST | `/api/v1/flo/:FloID` | Yes | Update flo |
| Flo | DELETE | `/api/v1/flo/:FloID` | Yes | Delete flo |
| Flo | POST | `/api/v1/flo/:FloID/revision` | Yes | Create flo revision |
| Flo | POST | `/api/v1/flo/:FloID/trigger/:TriggerID/execute` | No | Trigger flo execution |
| Execution | GET | `/api/v1/execution` | Yes | List executions |
| Execution | GET | `/api/v1/execution/:id` | Yes | Get execution by ID |
| Execution | POST | `/api/v1/execution/:id` | Runner | Update execution |
| Execution | POST | `/api/v1/execution/:id/state` | Runner | Update execution state |
| Runner | GET | `/api/v1/runner` | Yes | List runners |
| Runner | POST | `/api/v1/runner` | No | Register runner |
| Runner | POST | `/api/v1/runner/:id/execution` | Runner | Poll for executions |
| Runner | DELETE | `/api/v1/runner/:id` | Yes | Unregister runner |
| Queue | GET | `/api/v1/queue` | Yes | List queues |
| Environment | GET | `/api/v1/environment` | Yes | List environments |
| Environment | GET | `/api/v1/environment/:environment` | Yes | Get environment |
| Environment | POST | `/api/v1/environment` | Yes | Create environment |
| Environment | DELETE | `/api/v1/environment/:environment` | Yes | Delete environment |
| Environment | GET/POST/DELETE | `/api/v1/environment/:environment/property/...` | Yes | Manage environment properties |
| Environment | GET/POST/DELETE | `/api/v1/environment/:environment/secret/...` | Yes | Manage environment secrets |

## Development

```bash
# Run tests
make test

# Lint (runs go mod tidy, goimports, golangci-lint, go vet, gosec, govulncheck)
make lint

# Build for all platforms (linux, darwin, windows — amd64/arm64/arm)
make build
```

The build produces cross-compiled binaries in `dist/` with embedded version, git hash, and build date.

## Project Structure

```
.
├── cmd/
│   └── main.go                  # Application entrypoint
├── internal/
│   ├── actions/                 # Action definition service
│   ├── config/                  # Configuration loading (JSON/env/args)
│   ├── connector/
│   │   └── identity/            # Identity service connector
│   ├── http/                    # Gin HTTP handlers and routing
│   │   ├── service.go           # Router setup and middleware
│   │   ├── action.go            # Action endpoints
│   │   ├── dashboard.go         # Dashboard endpoints
│   │   ├── environment.go       # Environment endpoints
│   │   ├── execution.go         # Execution endpoints
│   │   ├── flow.go              # Flo endpoints
│   │   ├── organisation.go      # Organisation endpoints
│   │   ├── queue.go             # Queue endpoints
│   │   ├── runner.go            # Runner endpoints
│   │   └── user.go              # User endpoints
│   ├── persistence/             # PostgreSQL data access layer
│   │   ├── service.go           # Database queries (sqlx)
│   │   ├── migrations.go        # Embedded migration runner
│   │   └── migration/           # SQL migration files
│   ├── utils/                   # Utility functions
│   └── version/                 # Build version info
├── types.go                     # Shared domain types
├── project-metadata.json        # Package metadata for RPM/DEB builds
├── Dockerfile                   # Container image (Alpine, port 8888)
└── Makefile                     # Build, lint, test targets
```

## Licence

MIT — see [LICENCE.md](LICENCE.md).
