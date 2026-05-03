.PHONY: build test vet lint fmt complexity security licenses tidy ci clean all

build:
	go build ./...

test:
	go test ./... -count=1

vet:
	go vet ./...

fmt:
	gofmt -s -w .

lint:
	golangci-lint run

complexity:
	gocyclo -over 30 -ignore '_test\.go$$' .

security:
	gosec -exclude=G104,G115,G404 ./...

licenses:
	go-licenses report ./...

tidy:
	go mod tidy

ci: vet test complexity lint security

clean:
	rm -f junit-report.xml gosec-report.xml coverage.txt

all: build vet test
