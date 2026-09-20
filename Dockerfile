# AgentEmulator driver image. Ships the full Go toolchain and module source:
# agentsupervisor's chainrunner compiles the consensusnode/supervisor binaries
# at run time (`go build` into agentemu-bin/), so a static binary alone is not
# enough for a complete experiment inside the container.
FROM golang:1.25-alpine AS agentemu

WORKDIR /agentemulator

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENTRYPOINT ["go", "run", "./cmd/agentemu"]
CMD ["-config", "agentEmuConfig.yaml"]


# Chain-layer images (the BlockEmulator-X binaries that agentemu launches).
FROM golang:1.25-alpine AS builder

WORKDIR /agentemulator

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/consensusnode cmd/consensusnode/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/supervisor cmd/supervisor/main.go

FROM alpine:3 AS consensusnode

COPY --from=builder /bin/consensusnode /agentemulator/config.yaml /agentemulator/ip_table.json /

ENTRYPOINT ["/consensusnode"]
CMD ["-h"]


FROM alpine:3 AS supervisor

COPY --from=builder /bin/supervisor /agentemulator/config.yaml /agentemulator/ip_table.json /

ENTRYPOINT ["/supervisor"]
CMD ["-h"]
