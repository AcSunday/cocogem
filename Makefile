.PHONY: setup dev test probe-cli probe-isolation build contract check-contract e2e security release notices

setup:
	cd web && npm ci

contract:
	go run ./cmd/contract
	cd web && npm run generate

check-contract: contract
	git diff --exit-code -- api/openapi.yaml web/src/api/schema.ts

probe-cli:
	COCOGEM_CHECK_INSTALLED_CLAUDE=1 COCOGEM_CHECK_INSTALLED_CODEX=1 COCOGEM_CHECK_INSTALLED_GEMINI=1 go test -run 'TestInstalled(Claude|Codex|Gemini)ToolRegistry' -v ./internal/nativecli

probe-isolation:
	COCOGEM_CHECK_INSTALLED_ISOLATION=1 go test -run TestInstalledIsolationProbes -v ./internal/nativecli

test:
	node --test scripts/*.test.mjs
	test -z "$$(gofmt -l $$(find cmd internal -type f -name '*.go'))"
	go vet ./...
	go test -race -shuffle=on ./...
	go test -race -shuffle=on -tags=integration ./cmd/cocogem
	cd web && npm run lint && npm run typecheck && npm test

build:
	cd web && npm run build
	rm -rf dist/web
	mkdir -p dist/web
	go build -trimpath -o dist/cocogem ./cmd/cocogem
	go build -trimpath -o dist/cocogem-server ./cmd/cocogem-server
	cp -R web/dist/. dist/web/

dev: build
	node scripts/dev.mjs

e2e: build
	node --test scripts/dev.test.mjs
	go run ./cmd/smoke
	cd web && npm run test:e2e

security:
	go run ./cmd/checksecrets
	go tool govulncheck ./...
	cd web && npm audit --omit=dev

notices:
	node scripts/licenses.mjs

release: build notices
	go run ./cmd/release
