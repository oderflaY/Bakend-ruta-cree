# ---- build ----
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
RUN go build -ldflags="-s -w" -o /out/api ./cmd/api

# ---- runtime ----
# distroless/static: no shell, no package manager, runs as uid 65532. Nothing to exploit but our binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
COPY --chown=nonroot:nonroot seed /seed
ENV API_ADDR=:8080 SEED_DIR=/seed
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --start-period=20s --retries=3 CMD ["/api", "healthcheck"]
ENTRYPOINT ["/api"]
