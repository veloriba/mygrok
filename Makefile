# Deployment settings. There are intentionally NO insecure defaults here: real
# values must come from config.mk (copy config.mk.example). The check-config
# target fails fast if anything required is missing or left at a placeholder.
SERVER_USER ?=
SERVER_HOST ?=
DOMAIN ?=
TOKEN ?=
SUDO_PWD ?=

# Include local configuration if it exists
-include config.mk
-include .env

.PHONY: build build-server build-client clean server-install server-uninstall server-status cert-renew test run check-config win-client-build win-client-install win-client-uninstall win-client-status docker-build docker-buildx docker-save docker-up docker-down docker-status docker-logs

VERSION=$(shell cat VERSION)
LDFLAGS=-ldflags "-X github.com/veloriba/mygrok/internal/version.Version=$(VERSION)"

build: build-server build-client

test:
	go test -v ./...

build-server:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/mygrok-server cmd/server/main.go

build-client:
	go build $(LDFLAGS) -o bin/mygrok cmd/client/main.go

clean:
	rm -rf bin/

# Validate deployment config before touching a remote host. Refuses missing
# values and known-insecure placeholder tokens so we never push a weak secret.
check-config:
	@echo "mygrok: validating deployment config..."; \
	[ -n "$(SERVER_HOST)" ] || { echo "ERROR: SERVER_HOST not set (config.mk)" >&2; exit 1; }; \
	[ -n "$(SERVER_USER)" ] || { echo "ERROR: SERVER_USER not set (config.mk)" >&2; exit 1; }; \
	[ -n "$(DOMAIN)" ]      || { echo "ERROR: DOMAIN not set (config.mk)" >&2; exit 1; }; \
	[ -n "$(TOKEN)" ]       || { echo "ERROR: TOKEN not set (config.mk)" >&2; exit 1; }; \
	[ -n "$(SUDO_PWD)" ]    || { echo "ERROR: SUDO_PWD not set (config.mk/.env or environment)" >&2; exit 1; }; \
	case "$(TOKEN)" in secret-token-here|your-secret-token) echo "ERROR: refusing to deploy with the insecure placeholder TOKEN" >&2; exit 1;; esac

server-install: check-config
	@echo "--- Installing mygrok-server on $(SERVER_HOST) ---"
	SUDO_PWD='$(SUDO_PWD)' ./scripts/install-server.sh \
		--host $(SERVER_HOST) --user $(SERVER_USER) \
		--domain $(DOMAIN) --token $(TOKEN)

server-uninstall: check-config
	@echo "--- Uninstalling mygrok-server from $(SERVER_HOST) ---"
	SUDO_PWD='$(SUDO_PWD)' ./scripts/uninstall-server.sh \
		--host $(SERVER_HOST) --user $(SERVER_USER)

server-status:
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl status mygrok --no-pager"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo nginx -t && sudo systemctl status nginx --no-pager"

cert-renew:
	@echo "--- Starting Wildcard Certificate Renewal ---"
	@echo "Note: You will need to update the DNS TXT record manually when prompted."
	ssh -t $(SERVER_USER)@$(SERVER_HOST) "sudo certbot certonly --manual --preferred-challenges dns -d \"*.$(DOMAIN)\""
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl reload nginx"
	@echo "--- Certificate renewed and Nginx reloaded ---"

run: build-client
	./bin/mygrok --server $(SERVER_HOST):7000 http $(PORT) $(SUB)

# --- Docker (recommended client deployment) ---

DOCKER_DIR := deploy/docker
DOCKER_TAG ?= $(VERSION)

# Build the image for the current platform (tag: mygrok:$(DOCKER_TAG)).
docker-build:
	docker build -f $(DOCKER_DIR)/Dockerfile --build-arg VERSION=$(DOCKER_TAG) -t mygrok:$(DOCKER_TAG) .

# Build linux/amd64 + linux/arm64 and push to a registry.
# Usage: make docker-buildx DOCKER_REGISTRY=ghcr.io/veloriba
docker-buildx:
	docker buildx build -f $(DOCKER_DIR)/Dockerfile --build-arg VERSION=$(DOCKER_TAG) \
		--platform linux/amd64,linux/arm64 -t $(DOCKER_REGISTRY)mygrok:$(DOCKER_TAG) --push .

# Build and export a tarball for offline transfer: dist/mygrok-<ver>-<arch>.tar
docker-save:
	@arch=$$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/'); \
	docker build -f $(DOCKER_DIR)/Dockerfile --build-arg VERSION=$(DOCKER_TAG) -t mygrok:$(DOCKER_TAG) .; \
	mkdir -p dist; \
	docker save -o dist/mygrok-$(DOCKER_TAG)-$$arch.tar mygrok:$(DOCKER_TAG); \
	echo "Saved: dist/mygrok-$(DOCKER_TAG)-$$arch.tar"

# Start client tunnels defined in $(DOCKER_DIR)/docker-compose.client.yml
docker-up:
	docker compose -f $(DOCKER_DIR)/docker-compose.client.yml up -d

docker-down:
	docker compose -f $(DOCKER_DIR)/docker-compose.client.yml down

docker-status:
	docker compose -f $(DOCKER_DIR)/docker-compose.client.yml ps

docker-logs:
	docker compose -f $(DOCKER_DIR)/docker-compose.client.yml logs -f --tail 100 $(SERVICE)


# --- Windows host client deployment targets ---
# Requires WIN_HOST / WIN_USER / WIN_DIR in config.mk (see config.mk.example).

WIN_BIN := $(WIN_DIR)/mygrok.exe
WIN_SCHTASK ?= mygrok-client

win-client-build:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/mygrok.exe cmd/client/main.go

win-client-install: win-client-build
	@[ -n "$(WIN_HOST)" ] && [ -n "$(WIN_USER)" ] && [ -n "$(WIN_DIR)" ] || { echo "ERROR: WIN_HOST/WIN_USER/WIN_DIR not set (config.mk)" >&2; exit 1; }
	@echo "--- Installing mygrok client on $(WIN_HOST) ---"
	scp bin/mygrok.exe $(WIN_HOST):"$(WIN_BIN)"
	scp bin/config.json $(WIN_HOST):"$(WIN_DIR)/config.json"
	ssh $(WIN_HOST) "cmd /c schtasks /delete /tn $(WIN_SCHTASK) /f 2>nul; schtasks /create /tn $(WIN_SCHTASK) /tr \"\"\"$(WIN_BIN)\" --no-tui\" /sc onlogon /ru $(WIN_USER)"
	@echo "--- mygrok client installed, scheduled for auto-start on login ---"

win-client-uninstall:
	@[ -n "$(WIN_HOST)" ] && [ -n "$(WIN_DIR)" ] || { echo "ERROR: WIN_HOST/WIN_DIR not set (config.mk)" >&2; exit 1; }
	@echo "--- Uninstalling mygrok client from $(WIN_HOST) ---"
	ssh $(WIN_HOST) "cmd /c taskkill /F /IM mygrok.exe 2>nul; schtasks /delete /tn $(WIN_SCHTASK) /f 2>nul; del /q $(WIN_DIR)\mygrok.* 2>nul"
	@echo "--- Uninstalled successfully ---"

win-client-status:
	@[ -n "$(WIN_HOST)" ] || { echo "ERROR: WIN_HOST not set (config.mk)" >&2; exit 1; }
	ssh $(WIN_HOST) "cmd /c tasklist | findstr mygrok & schtasks /query /tn $(WIN_SCHTASK) 2>nul"
