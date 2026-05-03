# Default variables (can be overridden in config.mk or .env)
SERVER_USER ?= user
SERVER_HOST ?= vps.example.com
DOMAIN ?= example.com
TOKEN ?= secret-token-here

# Include local configuration if it exists
-include config.mk
-include .env

.PHONY: build build-server build-client clean server-install server-uninstall server-status cert-renew test run

VERSION=0.1.0

build: build-server build-client

test:
	go test -v ./...

build-server:
	GOOS=linux GOARCH=amd64 go build -o bin/mygrok-server cmd/server/main.go

build-client:
	go build -o bin/mygrok cmd/client/main.go

clean:
	rm -rf bin/

server-install: build-server
	@echo "--- Installing mygrok-server on $(SERVER_HOST) ---"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl stop mygrok || true"
	scp bin/mygrok-server $(SERVER_USER)@$(SERVER_HOST):~/mygrok-server
	
	# Create systemd service
	ssh $(SERVER_USER)@$(SERVER_HOST) "cat <<EOF | sudo tee /etc/systemd/system/mygrok.service > /dev/null\n\
	[Unit]\n\
	Description=mygrok server\n\
	After=network.target\n\
	\n\
	[Service]\n\
	ExecStart=/home/$(SERVER_USER)/mygrok-server -token $(TOKEN) -domain $(DOMAIN)\n\
	Restart=always\n\
	User=$(SERVER_USER)\n\
	\n\
	[Install]\n\
	WantedBy=multi-user.target\n\
	EOF"
	
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl daemon-reload && sudo systemctl enable mygrok && sudo systemctl start mygrok"
	@echo "--- Server installed and started ---"

server-uninstall:
	@echo "--- Uninstalling mygrok-server from $(SERVER_HOST) ---"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl stop mygrok || true"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl disable mygrok || true"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo rm -f /etc/systemd/system/mygrok.service"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo rm -f ~/mygrok-server"
	ssh $(SERVER_USER)@$(SERVER_HOST) "sudo systemctl daemon-reload"
	@echo "--- Uninstalled successfully ---"

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
