db      ?= gent.db
http    ?= :8080
tcp     ?=
uds     ?=
poll    ?= 500
log     ?= info

# BUILD_FLAGS = CGO_ENABLED=1

.PHONY: run build test test-unit test-int test-stress bench-recursive bench-deep swagger client clean generate

run:
	$(BUILD_FLAGS) go run ./cmd/gent \
		-db $(db) \
		-http $(http) \
		$(if $(tcp),-tcp $(tcp)) \
		$(if $(uds),-uds $(uds)) \
		-poll $(poll) \
		-log $(log) \
		$(ARGS)

build: sqlc
	$(BUILD_FLAGS) go build -tags "sqlite_omit_load_extension" -ldflags="-s -w" -o gent ./cmd/gent
	$(BUILD_FLAGS) go build -ldflags="-s -w" -o gentctl ./cmd/gentctl

test: test-unit test-int

test-unit:
	$(BUILD_FLAGS) go test ./...

test-stress:
	$(BUILD_FLAGS) go test ./internal/db/... ./internal/engine/... -run TestStress -v --count=3

swagger:
	$(BUILD_FLAGS) go run ./cmd/gentspec

schema:
	$(BUILD_FLAGS) go run ./cmd/gentschema $(ARGS)

client: swagger
	cd tests && bun run generate

test-int: client
	cd tests && ~/.bun/bin/bun run typecheck && ~/.bun/bin/bun run test

# Spawn benchmarks: YAML-defined workloads (tests/bench/workloads/), SQLite vs Postgres.
# bench-recursive — full binary tree (wide); measures concurrent throughput ceiling.
# bench-deep      — narrow/tall tree; measures per-spawn depth cost.
# Defaults are sized to the same instance count (~8k) so the shapes compare directly.
# Tunables: BENCH_TTL, BENCH_ROOTS, BENCH_POLL_MS, BENCH_MAX_CONCURRENT, BENCH_RUNS,
# BENCH_TIMEOUT_MS. Set POSTGRES_DSN to also benchmark Postgres.
bench-recursive: client
	cd tests && ~/.bun/bin/bun run bench-recursive

bench-deep: client
	cd tests && ~/.bun/bin/bun run bench-deep

sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate

clean:
	rm -f gent gentctl $(db)
