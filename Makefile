GOFLAGS = -ldflags "-s -w" -tags lambda.norpc

main:
	go build -o bootstrap $(GOFLAGS) cmd/main.go
	zip bootstrap.zip bootstrap
