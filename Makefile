PROJECT_NAME := miniwiki
GO := go
SSH_KEY_PATH = ./miniwiki
BIN_DIR = bin

.PHONY: all create_ssh_key build run clean

all: clean build create_ssh_key run

create_ssh_key:
	@if [ ! -f $(SSH_KEY_PATH) ]; then \
		ssh-keygen -N "" -f $(SSH_KEY_PATH); \
	fi

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(PROJECT_NAME) ./...

run:
	./$(BIN_DIR)/$(PROJECT_NAME)

clean:
	rm -rf $(BIN_DIR)
