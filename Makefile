APP := babyone_check_service
BUILD_DIR := build

.PHONY: build armv7 arm64 amd64 clean

build:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP) .

armv7:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP)-armv7 .

arm64:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP)-arm64 .

amd64:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w" -o $(BUILD_DIR)/$(APP)-amd64 .

clean:
	rm -rf $(BUILD_DIR)