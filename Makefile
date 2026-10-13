# ChapterSmith - Makefile
#
# Prerequisites:
#   - Go 1.27+
#   - pkg-config; nasm on x86_64 (FFmpeg asm)
#
# Quick start:
#   make ffmpeg    # Build FFmpeg LGPL static archives (one-time)
#   make build     # Build CLI for current platform (FFmpeg linked into it)
#   make package   # Build GUI + CLI installers for current platform

BINARY_NAME := chaptersmith
MODULE      := chaptersmith
WAILS_VERSION := $(shell sed -n 's/^[[:space:]]*github.com\/wailsapp\/wails\/v3 \([^[:space:]]*\).*/\1/p' go.mod)
# vendor/ also holds FFmpeg; resolve Go dependencies from go.mod, not stale vendor copies.
export GOFLAGS ?= -mod=mod
GO_BUILD_FLAGS ?=
LINT_BUILD_FLAGS :=
CMD_PATH    := ./cmd/chaptersmith
DIST_DIR    := dist

# FFmpeg vendor paths
FFMPEG_PREFIX ?= $(CURDIR)/vendor/ffmpeg
PKG_CONFIG_PATH := $(FFMPEG_PREFIX)/lib/pkgconfig

# Version info (injected at build time)
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME  := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS     := -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

# CGO configuration: the adapter adds Libs.private only for FFmpeg.
export CGO_ENABLED := 1
export CGO_CFLAGS  := -I$(FFMPEG_PREFIX)/include
export PKG_CONFIG_PATH
export FFMPEG_PREFIX
export PKG_CONFIG := "$(CURDIR)/vendor/ffmpeg-tools/pkg-config"

UNAME_S := $(shell uname -s)
EXE :=
ifeq ($(UNAME_S),Darwin)
    # Go 1.27 itself targets macOS 13 (cmd/link macho.go), so we cannot go lower.
    export MACOSX_DEPLOYMENT_TARGET := 13.0
    # Wails adds a lower deployment target; override it on the final linker invocation.
    LDFLAGS += -extldflags=-mmacosx-version-min=13.0
else ifeq ($(UNAME_S),Linux)
    # Wails beta defaults to GTK4. Retain GTK3/WebKit 4.1 for Ubuntu 22.04.
    GO_BUILD_FLAGS += -tags=gtk3
    LINT_BUILD_FLAGS := --build-tags=gtk3
else
    # Windows (MSYS2): native Go/GCC require mixed-mode paths.
    # Native Go/GCC subprocesses need Windows paths, not MSYS /d/... paths.
    override FFMPEG_PREFIX := $(shell cygpath -m "$(FFMPEG_PREFIX)")
    PKG_CONFIG_PATH := $(FFMPEG_PREFIX)/lib/pkgconfig
    export CGO_CFLAGS := -I$(FFMPEG_PREFIX)/include
    export PKG_CONFIG := "$(shell cygpath -m "$(CURDIR)/vendor/ffmpeg-tools/pkg-config.exe")"
    EXE := .exe
endif
# zlib / OS frameworks remain system dependencies; do not globally use -static.
export CGO_LDFLAGS :=


.PHONY: all build clean test ffmpeg ffmpeg-clean check-ffmpeg build-gui _bundle-macos package help dev dev-frontend dev-gui install-wails3

all: build

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-25s\033[0m %s\n", $$1, $$2}'

gui-deps: ## Install frontend dependencies for GUI
	@cd frontend && npm install


ffmpeg: ## Build FFmpeg n8.0 LGPL static archives into vendor/ffmpeg/
	@echo "==> Building FFmpeg static archives..."
	@./scripts/build-ffmpeg.sh --prefix="$(FFMPEG_PREFIX)"

ffmpeg-clean: ## Remove FFmpeg build artifacts (keeps Go vendor/)
	@echo "==> Cleaning FFmpeg build..."
	@rm -rf vendor/ffmpeg vendor/ffmpeg-build vendor/ffmpeg-tools

check-ffmpeg:
	@./scripts/prepare-static-ffmpeg.sh


build: check-ffmpeg ## Build CLI for current platform
	@echo "==> Building $(BINARY_NAME) for current platform..."
	@mkdir -p $(DIST_DIR)
	go build $(GO_BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o $(DIST_DIR)/$(BINARY_NAME)$(EXE) $(CMD_PATH)
	@./scripts/collect-ffmpeg-libs.sh "$(FFMPEG_PREFIX)" "$(DIST_DIR)"
	@echo "==> Built: $(DIST_DIR)/$(BINARY_NAME)$(EXE)"

GUI_LDFLAGS := $(LDFLAGS)
ifeq ($(UNAME_S),MINGW64_NT)
    GUI_LDFLAGS += -H windowsgui
endif
ifeq ($(findstring MSYS,$(UNAME_S)),MSYS)
    GUI_LDFLAGS += -H windowsgui
endif
ifeq ($(OS),Windows_NT)
    GUI_LDFLAGS += -H windowsgui
endif

build-gui: check-ffmpeg ## Build GUI application (requires: make ffmpeg, npm)
	@echo "==> Building frontend..."
	@cd frontend && npm ci && npm run build
	@echo "==> Copying frontend to embed dir..."
	@rm -rf cmd/chaptersmith-gui/embed && mkdir -p cmd/chaptersmith-gui/embed && cp -r frontend/dist/* cmd/chaptersmith-gui/embed/
	@echo "==> Building $(BINARY_NAME)-gui..."
	@mkdir -p $(DIST_DIR)
	go build $(GO_BUILD_FLAGS) -ldflags '$(GUI_LDFLAGS)' -o $(DIST_DIR)/$(BINARY_NAME)-gui$(EXE) ./cmd/chaptersmith-gui
	@./scripts/collect-ffmpeg-libs.sh "$(FFMPEG_PREFIX)" "$(DIST_DIR)"
	@echo "==> Built: $(DIST_DIR)/$(BINARY_NAME)-gui"
ifeq ($(UNAME_S),Darwin)
	@$(MAKE) _bundle-macos
endif

_bundle-macos:
	@echo "==> Creating macOS .app bundle..."
	@rm -rf "$(DIST_DIR)/ChapterSmith.app"
	@mkdir -p "$(DIST_DIR)/ChapterSmith.app/Contents/MacOS"
	@mkdir -p "$(DIST_DIR)/ChapterSmith.app/Contents/Resources"
	@cp $(DIST_DIR)/$(BINARY_NAME)-gui "$(DIST_DIR)/ChapterSmith.app/Contents/MacOS/chaptersmith-gui"
	@cp build/darwin/icon.icns "$(DIST_DIR)/ChapterSmith.app/Contents/Resources/icon.icns"
	@sed 's/__VERSION__/$(VERSION)/g' build/darwin/Info.plist > "$(DIST_DIR)/ChapterSmith.app/Contents/Info.plist"
	@echo "==> Bundled: $(DIST_DIR)/ChapterSmith.app"
	@echo "    Launch with: open \"$(DIST_DIR)/ChapterSmith.app\""

package: build build-gui ## Build GUI + CLI installers for the current platform into dist/packages
ifeq ($(UNAME_S),Darwin)
	./scripts/package-macos.sh gui
	./scripts/package-macos.sh cli
else ifeq ($(UNAME_S),Linux)
	./scripts/package-linux.sh gui
	./scripts/package-linux.sh cli
else
	bash ./scripts/package-windows.sh gui
	bash ./scripts/package-windows.sh cli
endif


test: check-ffmpeg ## Run tests
	go test $(GO_BUILD_FLAGS) -ldflags '$(LDFLAGS)' -v -race -count=1 ./...

lint: ## Run linter (requires golangci-lint)
	golangci-lint run $(LINT_BUILD_FLAGS) ./...


clean: ## Remove build artifacts
	@echo "==> Cleaning build artifacts..."
	@rm -rf $(DIST_DIR)
	@echo "==> Clean complete."

clean-all: clean ffmpeg-clean ## Remove all artifacts including FFmpeg
	@echo "==> Full clean complete."


deps: ## Download Go module dependencies
	go mod tidy
	go mod download

install-wails3: ## Install Wails v3 CLI tool
	go install github.com/wailsapp/wails/v3/cmd/wails3@$(WAILS_VERSION)

dev-frontend: gui-deps ## Start Vite dev server for frontend (port 5173)
	@echo "==> Starting Vite dev server on http://localhost:5173 ..."
	@cd frontend && BOOK_NATIVE_DEV=1 npx vite --host

dev-gui: check-ffmpeg ## Build and run GUI in dev mode (connects to Vite at localhost:5173)
	@echo "==> Building GUI binary (dev mode)..."
	@mkdir -p $(DIST_DIR)
	go build $(GO_BUILD_FLAGS) -ldflags '$(GUI_LDFLAGS)' -o $(DIST_DIR)/$(BINARY_NAME)-gui ./cmd/chaptersmith-gui
ifeq ($(UNAME_S),Darwin)
	@$(MAKE) _bundle-macos
	@echo "==> Running GUI in dev mode (macOS .app)..."
	open "$(DIST_DIR)/ChapterSmith.app" --args -dev
else
	@echo "==> Running GUI in dev mode..."
	$(DIST_DIR)/$(BINARY_NAME)-gui -dev
endif

dev: check-ffmpeg ## Run full dev setup: build Go backend and launch with -dev (start 'make dev-frontend' first!)
	@echo "============================================"
	@echo "  Make sure 'make dev-frontend' is running"
	@echo "  in another terminal first!"
	@echo "============================================"
	@echo ""
	@echo "==> Building and running GUI in dev mode..."
	@mkdir -p $(DIST_DIR)
	go build $(GO_BUILD_FLAGS) -ldflags '$(GUI_LDFLAGS)' -o $(DIST_DIR)/$(BINARY_NAME)-gui ./cmd/chaptersmith-gui
ifeq ($(UNAME_S),Darwin)
	@$(MAKE) _bundle-macos
	open "$(DIST_DIR)/ChapterSmith.app" --args -dev
else
	$(DIST_DIR)/$(BINARY_NAME)-gui -dev
endif

check-tools: ## Verify required tools are installed
	@echo "Checking required tools..."
	@command -v go >/dev/null 2>&1 && echo "  go:   $(shell go version)" || echo "  go:   NOT FOUND"
	@command -v nasm >/dev/null 2>&1 && echo "  nasm: $(shell nasm -v 2>&1 | head -1)" || echo "  nasm: NOT FOUND (needed on x86_64 for FFmpeg)"
	@command -v pkg-config >/dev/null 2>&1 && echo "  pkg-config: found" || echo "  pkg-config: NOT FOUND"
	@command -v git >/dev/null 2>&1 && echo "  git:  $(shell git --version)" || echo "  git:  NOT FOUND"
