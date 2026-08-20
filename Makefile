# my-k8s-commander: build tất cả cmd/ — supervisor ra root, workers vào modules/
BINARY_SUPERVISOR := my-k8s-commander
MODULES_DIR      := modules

ifeq ($(OS),Windows_NT)
  BINARY_SUPERVISOR := my-k8s-commander.exe
  AI_WORKER         := $(MODULES_DIR)/ai-worker.exe
  K8S_WORKER        := $(MODULES_DIR)/k8s-worker.exe
  CONSOLE_WORKER    := $(MODULES_DIR)/console-worker.exe
  SERVER_WORKER     := $(MODULES_DIR)/server-worker.exe
  SWARM_WORKER      := $(MODULES_DIR)/swarm-worker.exe
  FFI_LIB           := myk8s_commander.dll
else
  AI_WORKER         := $(MODULES_DIR)/ai-worker
  K8S_WORKER        := $(MODULES_DIR)/k8s-worker
  CONSOLE_WORKER    := $(MODULES_DIR)/console-worker
  SERVER_WORKER     := $(MODULES_DIR)/server-worker
  SWARM_WORKER      := $(MODULES_DIR)/swarm-worker
  ifeq ($(shell uname -s),Darwin)
    FFI_LIB         := libmyk8s_commander.dylib
  else
    FFI_LIB         := libmyk8s_commander.so
  endif
endif

FFI_HEADER := $(basename $(FFI_LIB)).h

.PHONY: all app build build-supervisor build-modules build-ffi clean clean-all run smoke

all: build build-ffi

build: build-supervisor build-modules

build-supervisor:
	CGO_ENABLED=1 go build -o $(BINARY_SUPERVISOR) ./cmd/supervisor

build-modules: | $(MODULES_DIR)
	go build -o $(AI_WORKER) ./cmd/module-ai
	go build -o $(K8S_WORKER) ./cmd/module-k8s
	go build -o $(CONSOLE_WORKER) ./cmd/module-console
	go build -o $(SERVER_WORKER) ./cmd/module-server
	go build -o $(SWARM_WORKER) ./cmd/module-swarm

$(MODULES_DIR):
	mkdir -p $(MODULES_DIR)

# Shared library cho Flutter FFI: StartCore/GetLogs/SendToModule/StopCore.
build-ffi:
	CGO_ENABLED=1 go build -buildmode=c-shared -o $(FFI_LIB) ./cmd/supervisor

# App macOS tự chứa (chỉ macOS). `flutter build macos` KHÔNG tự nhét core vào
# bundle — không copy thì app chỉ chạy được ngay trong repo, nhờ đường lui đi
# ngược cây thư mục của native_core.dart; đem đi chỗ khác là hỏng.
# Copy xong phải ký lại: sửa file bên trong làm hỏng chữ ký của Flutter.
APP_BUNDLE := build/macos/Build/Products/Release/k8s_commander_app.app

app: all
	flutter build macos --release
	cp $(FFI_LIB) "$(APP_BUNDLE)/Contents/Frameworks/"
	rm -rf "$(APP_BUNDLE)/Contents/Resources/modules"
	mkdir -p "$(APP_BUNDLE)/Contents/Resources/modules"
	cp $(MODULES_DIR)/* "$(APP_BUNDLE)/Contents/Resources/modules/"
	codesign --force --sign - "$(APP_BUNDLE)/Contents/Resources/modules/"*
	codesign --force --sign - "$(APP_BUNDLE)/Contents/Frameworks/$(FFI_LIB)"
	codesign --force --sign - "$(APP_BUNDLE)"
	@echo "-> $(APP_BUNDLE)"

clean:
	rm -f $(FFI_LIB) $(FFI_HEADER)
	rm -f $(BINARY_SUPERVISOR) $(BINARY_SUPERVISOR).exe
	rm -f $(MODULES_DIR)/ai-worker $(MODULES_DIR)/ai-worker.exe
	rm -f $(MODULES_DIR)/k8s-worker $(MODULES_DIR)/k8s-worker.exe
	rm -f $(MODULES_DIR)/console-worker $(MODULES_DIR)/console-worker.exe
	rm -f $(MODULES_DIR)/server-worker $(MODULES_DIR)/server-worker.exe
	rm -f $(MODULES_DIR)/swarm-worker $(MODULES_DIR)/swarm-worker.exe

# clean-all thêm cache của Flutter (build/ + .dart_tool/, cỡ 400MB). Tách riêng
# khỏi `clean` vì dựng lại tốn vài phút.
clean-all: clean
	flutter clean

run: build-supervisor
	./$(BINARY_SUPERVISOR)

# Kiểm tra round-trip FFI (StartCore -> SendToModule -> GetLogs) không cần GUI.
smoke: build build-ffi
	K8SC_CORE_ROOT=$(CURDIR) dart run tool/core_smoke.dart
