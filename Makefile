# Karabiner-DriverKit-VirtualHIDDevice release whose client headers the bridge
# compiles against. Keep it equal to the installed driver package: the daemon
# rejects clients with a different client_protocol_version.
KARABINER_TAG ?= v8.6.0
KARABINER_DIR := bridge/third_party/karabiner

BIN := bin

.PHONY: all bridge service test clean

all: bridge service

$(KARABINER_DIR):
	git clone --depth 1 --branch $(KARABINER_TAG) \
		https://github.com/pqrs-org/Karabiner-DriverKit-VirtualHIDDevice.git $(KARABINER_DIR)

bridge: $(KARABINER_DIR)
	mkdir -p $(BIN)
	clang++ -std=c++23 -O2 -Wall \
		-I$(KARABINER_DIR)/include \
		-isystem $(KARABINER_DIR)/vendor/vendor/include \
		bridge/vnkvm-hid-bridge.cpp \
		-framework IOKit -framework CoreFoundation \
		-o $(BIN)/vnkvm-hid-bridge

service:
	mkdir -p $(BIN)
	go build -o $(BIN)/vnkvm ./cmd/vnkvm

test:
	go test ./...

clean:
	rm -rf $(BIN)
