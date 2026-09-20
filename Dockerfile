# Build stage: compiles the server into a single statically linked binary.
FROM golang:1.26.1-bookworm AS build

WORKDIR /src

# Dependency manifest is copied alone so go mod download stays cached across source-only edits.
COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/server ./cmd/server

# Runtime stage: distroless static carries no libc, which is valid only because CGO is disabled above.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /server

ENV PORT=8080

EXPOSE 8080

USER nonroot

ENTRYPOINT ["/server"]
