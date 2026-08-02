INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build frontend dev clean install

# Build the frontend and embed it in the Go binary
build: frontend
	go build -o dist/termyard .

# Build just the frontend
frontend:
	cd web && npm ci && npm run build

# Development mode - run Go server with live reload
dev:
	go run . server

# Install built binary — handles "text file busy" from running daemons.
# rm unlinks the old inode; running processes keep their handle, but the
# path is freed so cp can write a new file.
install: build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/termyard
	cp dist/termyard $(INSTALL_DIR)/termyard
	@echo "Installed to $(INSTALL_DIR)/termyard"
	@echo "Restart the service: systemctl --user restart termyard"

# Clean build artifacts
clean:
	rm -rf dist/
	rm -rf web/node_modules/
	rm -rf pkg/server/dist/*
	touch pkg/server/dist/.gitkeep
