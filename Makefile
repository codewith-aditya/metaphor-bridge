BINARY := metaphor-go

.PHONY: build run install test vet fmt clean

build:
	go build -o $(BINARY) .

run: build
	./$(BINARY) serve

vet:
	go vet ./...

fmt:
	gofmt -w *.go

install: build
	install -m 0755 $(BINARY) /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY)

# quick smoke test against a running bridge
test:
	curl -sS http://127.0.0.1:8190/healthz
	curl -sS http://127.0.0.1:8190/v1/models | head -c 200
