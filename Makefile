# Build and test targets for stompwatch.
#
# The ZimaBoard runs Debian 13 on x86-64, so build is a static linux/amd64
# binary with no cgo. Override GOOS or GOARCH for another target.

BIN := bin/stompwatch
GOOS ?= linux
GOARCH ?= amd64

# Host for make deploy and make install-service, as an ssh target. It has no
# default, so name the box every time. Only the command line sets it: a HOST
# in the environment is ignored.
HOST :=

.DEFAULT_GOAL := build
.PHONY: build web web-dev deploy install-service check-host test race check fmt vet clean

# The files that decide whether web/dist is out of date.
WEB_SRC := $(shell find web/src web/public web/index.html web/package.json \
	web/vite.config.ts web/tsconfig.app.json -type f 2>/dev/null)

# build: static binary at bin/stompwatch. The web interface is built first,
# because the binary embeds web/dist.
build: web
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -o $(BIN) ./cmd/stompwatch

# web: build the dashboard into web/dist when a source file is newer than the
# built index.html. Node is a build-time dependency only, and web/dist is
# committed, so a box with no Node still builds the binary from the committed
# files. It says which of the two happened.
web: web/dist/index.html

web/dist/index.html: $(WEB_SRC)
	@if [ ! -f web/package.json ]; then \
		echo "no web/package.json; keeping web/dist as it is"; \
		test -f $@ || { echo "and there is nothing in web/dist to embed"; exit 1; }; \
	elif command -v npm >/dev/null 2>&1; then \
		echo "building the web interface"; \
		cd web && npm install --no-audit --no-fund && npm run build; \
	elif [ -f $@ ]; then \
		echo "npm is not installed, so this build uses the committed web/dist."; \
		echo "Install Node if you changed anything under web/src."; \
	else \
		echo "npm is not installed and web/dist is empty."; \
		echo "Install Node, or check out a commit that carries web/dist."; \
		exit 1; \
	fi
	@touch $@

# web-dev: the Vite dev server, with /api proxied to a running collector.
# Set STOMPWATCH_API when the collector listens somewhere other than
# 127.0.0.1:8080, such as tools/devserver on another port.
web-dev:
	cd web && npm install --no-audit --no-fund && npm run dev

# The files that root installs on HOST go through a private directory that
# mktemp makes there, mode 0700, and never through the shared /tmp, where
# another local user could plant or swap them first. The directory is
# removed afterwards, also when a step fails.

# deploy: build, copy to HOST, and install. It restarts the service only if
# the service is already running, and says which of the two happened.
deploy: check-host build
	set -e; dir=$$(ssh $(HOST) mktemp -d); \
	trap 'ssh $(HOST) rm -rf "$$dir"' EXIT; \
	scp $(BIN) $(HOST):"$$dir/stompwatch"; \
	ssh -t $(HOST) "set -e; sudo install -m 0755 $$dir/stompwatch /usr/local/bin/stompwatch; if systemctl is-active --quiet stompwatch; then sudo systemctl restart stompwatch; echo 'service restarted'; else echo 'binary installed; the service is not running'; fi"

# install-service: create the user, directories, config, and systemd unit on
# HOST. It never overwrites an existing config and starts nothing.
install-service: check-host
	set -e; dir=$$(ssh $(HOST) mktemp -d); \
	trap 'ssh $(HOST) rm -rf "$$dir"' EXIT; \
	scp deploy/stompwatch.service deploy/stompwatch.conf.example deploy/camera.env.example deploy/install.sh $(HOST):"$$dir/"; \
	ssh -t $(HOST) "sudo sh $$dir/install.sh $$dir"

check-host:
	@test -n "$(HOST)" || { echo "set HOST, for example: make deploy HOST=user@box-ip-or-address"; exit 1; }

# test: run every test.
test:
	go test ./...

# race: run every test with the race detector.
race:
	go test -race ./...

# check: everything that must pass before a commit.
check: fmt vet race

fmt:
	@test -z "$$(gofmt -l .)" || { echo "these files need gofmt:"; gofmt -l .; exit 1; }

vet:
	go vet ./...

clean:
	rm -rf bin
