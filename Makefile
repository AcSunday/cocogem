.PHONY: setup dev test build demo contract check-contract e2e security release notices

setup:
	cd web && npm ci

contract:
	go run ./cmd/contract
	cd web && npm run generate

check-contract: contract
	git diff --exit-code -- api/openapi.yaml web/src/api-types.ts

test:
	node --test scripts/*.test.mjs
	test -z "$$(gofmt -l $$(git ls-files '*.go'))"
	go vet ./...
	go test -race -shuffle=on ./...
	cd web && npm run lint && npm run typecheck && npm test

build:
	cd web && npm run build
	rm -rf dist/web
	mkdir -p dist/web
	go build -trimpath -o dist/cocogem-server ./cmd/cocogem-server
	cp -R web/dist/. dist/web/

demo: build
	./dist/cocogem-server --demo

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
