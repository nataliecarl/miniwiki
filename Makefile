PROJECT_NAME := "miniwiki"
GO := go

.PHONY: all build run clean

all: build

build:
	$(GO) build -o bin/$(PROJECT_NAME) ./...

run:
	./bin/$(PROJECT_NAME)

clean:
	rm -rf bin
