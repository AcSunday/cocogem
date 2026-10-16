.PHONY: setup dev test build demo contract check-contract e2e security release

setup:
	cd web && npm ci

contract:
	go run ./cmd/contract
	cd web && npm run generate

check-contract: contract
	git diff --exit-code -- api/openapi.yaml web/src/api-types.ts

test:
	test -z "$$(gofmt -l $$(git ls-files '*.go'))"
	go vet ./...
	go test -race -shuffle=on ./...
	cd web && npm run lint && npm run typecheck && npm test

build:
	cd web && npm run build
	mkdir -p dist/web
	go build -trimpath -o dist/cocogem-server ./cmd/cocogem-server
	cp -R web/dist/. dist/web/

demo: build
	./dist/cocogem-server --demo

dev: build
	node scripts/dev.mjs

e2e: build
	cd web && npm run test:e2e

security:
	go tool govulncheck ./...
	cd web && npm audit --omit=dev

release: build
	go run ./cmd/release
