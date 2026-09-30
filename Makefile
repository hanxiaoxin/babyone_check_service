APP := babyone_check_service
BUILD_DIR := build

export CGO_ENABLED := 0

ifeq ($(OS),Windows_NT)
MKDIR = if not exist "$(BUILD_DIR)" mkdir "$(BUILD_DIR)"
CLEAN = if exist "$(BUILD_DIR)" rmdir /s /q "$(BUILD_DIR)"
else
MKDIR = mkdir -p "$(BUILD_DIR)"
CLEAN = rm -rf "$(BUILD_DIR)"
endif

.PHONY: build armv7 arm64 amd64 windows clean

build: armv7

armv7: export GOOS := linux
armv7: export GOARCH := arm
armv7: export GOARM := 7
armv7:
	$(MKDIR)
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP)-armv7 .

arm64: export GOOS := linux
arm64: export GOARCH := arm64
arm64:
	$(MKDIR)
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP)-arm64 .

amd64: export GOOS := linux
amd64: export GOARCH := amd64
amd64:
	$(MKDIR)
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP)-amd64 .

windows: export GOOS := windows
windows: export GOARCH := amd64
windows:
	$(MKDIR)
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP).exe .

clean:
	$(CLEAN)