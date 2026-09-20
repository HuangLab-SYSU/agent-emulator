AGENTEMU_IMAGE=agentemulator/agentemu:latest
CONSENSUS_IMAGE=agentemulator/consensusnode:latest
SUPERVISOR_IMAGE=agentemulator/supervisor:latest

.PHONY: all
all: test lint-fix build-image

.PHONY: test
test:
	go test -gcflags=all='-N -l' ./...

.PHONY: lint-fix
lint-fix:
	golangci-lint run ./... --fix

.PHONY: run
run:
	bash run_agentemu.sh $(CONFIG)

.PHONY: build-image
build-image:
	docker build -t $(AGENTEMU_IMAGE) --target agentemu .
	docker build -t $(CONSENSUS_IMAGE) --target consensusnode .
	docker build -t $(SUPERVISOR_IMAGE) --target supervisor .

.PHONY: run-agentemu
run-agentemu:
	docker run --rm $(AGENTEMU_IMAGE) $(ARGS)

.PHONY: run-consensus
run-consensus:
	docker run --rm $(CONSENSUS_IMAGE) $(ARGS)

.PHONY: run-supervisor
run-supervisor:
	docker run --rm $(SUPERVISOR_IMAGE) $(ARGS)

.PHONY: docs-pdf2svg
docs-pdf2svg:
	sh ./docs/scripts/pdf2svg.sh
