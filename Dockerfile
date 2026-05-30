# syntax=docker/dockerfile:1

# --- build stage ---
FROM golang:1.26 AS build
WORKDIR /src

# Cache module downloads on an isolated layer.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary (pgx is pure Go, so CGO is unnecessary).
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/webhook-ingestor .

# --- run stage ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/webhook-ingestor /webhook-ingestor
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/webhook-ingestor"]
