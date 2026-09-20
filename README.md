# UniMap (backend)

UniMap is a photo-first travel-memory app. The backend stores uploaded photos and
extracts their geolocation and timestamp from EXIF metadata, so memories place
themselves on a map without manual tagging. On top of that it serves the
map-based main view, a search bar that filters existing content, flexible
grouping by city, country or custom collections, per-item privacy settings, and
the profile page. This repository currently holds only the service skeleton: an
HTTP server with a health endpoint.

## Prerequisites

- Go 1.26.1 or newer

## Run

```sh
go run ./cmd/server
```

Listens on `:8080`, or on `$PORT` when that variable is set.

## Test

```sh
go test ./...
```

## Endpoints

| Method | Path       | Response                    |
| ------ | ---------- | --------------------------- |
| GET    | `/healthz` | `200` `{"status":"ok"}`     |

## Open decisions

These are not settled yet and nothing in this repository commits to an answer:

- **Database** — MongoDB or PostgreSQL.
- **API style** — GraphQL or REST.
- **Photo blob storage** — not yet discussed.

Deployment target is Google Cloud with GitHub Actions.
