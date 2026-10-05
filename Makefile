.PHONY: build vet fmt test race run clean

build:
	go build ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

test:
	go test ./...

race:
	go test -race ./...

run:
	go run ./cmd/gostalgia boot

clean:
	rm -f gostalgia gctl
	rm -rf gs-root
