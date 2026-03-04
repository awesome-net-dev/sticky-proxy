.PHONY: build test lint vet fmt docker-build docker-up docker-down clean

build:
	go build -o bin/proxy ./cmd/proxy
	go build -o bin/backend ./cmd/backend

test:
	go test ./... -race -count=1

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "Files not formatted:" && gofmt -l . && exit 1)

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

clean:
	rm -rf bin/
