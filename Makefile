BINARY  := desklinkd
CLIENT  := desklink
APK     := desklink.apk
# monova fails until the first commit exists; 0.0.0 keeps local builds working.
VERSION := $(shell monova 2>/dev/null || echo 0.0.0)
LDFLAGS := -ldflags="-X main.Version=$(VERSION)"

.NOTPARALLEL: check android-check

export PATH := $(PATH):$(shell go env GOPATH)/bin

GST_CHECK = @pkg-config --exists gstreamer-1.0 gstreamer-app-1.0 gstreamer-video-1.0 gtk4 libadwaita-1 || { \
	echo "GStreamer or GTK development files are missing. Install them with:"; \
	echo "  sudo dnf install gstreamer1-devel gstreamer1-plugins-base-devel gtk4-devel libadwaita-devel"; \
	exit 1; }

test:
	$(GST_CHECK)
	cd host && CGO_ENABLED=1 go test ./...

vet:
	$(GST_CHECK)
	cd host && CGO_ENABLED=1 go vet ./...

fmt:
	@command -v goimports >/dev/null 2>&1 || { \
	  echo "goimports is not installed. Install it with:"; \
	  echo "  go install golang.org/x/tools/cmd/goimports@latest"; \
	  exit 1; \
	}
	cd host && goimports -w .

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "golangci-lint is not installed. Install it with:"; \
	  echo "  grm install golangci/golangci-lint"; \
	  exit 1; \
	}
	cd host && golangci-lint run ./...

# Every agent session loads AGENTS.md whole, and Codex reads only its first
# 32 KiB: keep it under the limit (AGENTS.universal.md, "AGENTS.md size").
AGENTS_MAX_BYTES ?= 30000
agents-size:
	@size=$$(wc -c < AGENTS.md); \
	if [ "$$size" -gt $(AGENTS_MAX_BYTES) ]; then \
	  echo "AGENTS.md is $$size bytes, over the $(AGENTS_MAX_BYTES)-byte limit: move detail to docs/ (AGENTS.universal.md, \"AGENTS.md size\")"; \
	  exit 1; \
	fi

build:
	$(GST_CHECK)
	@mkdir -p bin
	cd host && CGO_ENABLED=1 go build $(LDFLAGS) -o ../bin/$(BINARY) ./cmd/desklinkd
	cd host && CGO_ENABLED=1 go build $(LDFLAGS) -o ../bin/$(CLIENT) ./cmd/desklink

# The laptop client's GUI smoke test, on a private virtual screen, bus and
# runtime directory, so nothing it starts reaches the desktop of whoever runs
# it. Run every other GUI command through scripts/headless-gui.sh too.
smoke:
	cd host && DESKLINK_UI_SMOKE=1 ../scripts/headless-gui.sh go test ./internal/viewer -count=1

check: fmt vet build test smoke lint agents-size
	@echo "==> make check: all green"

# Gradle runs the build on JDK 25 (android/gradle/gradle-daemon-jvm.properties),
# downloading it when missing, and installs the pinned SDK platform and build
# tools once the SDK licence is recorded.
ANDROID_GRADLE = set -e; \
	if [ -z "$$JAVA_HOME" ] && ! command -v java >/dev/null 2>&1; then \
	  echo "Install a Java runtime to start Gradle with: sudo dnf install java-25-openjdk-headless"; exit 1; \
	fi; \
	SDK=$${ANDROID_HOME:-$$(sed -n 's/^sdk.dir=//p' android/local.properties 2>/dev/null)}; \
	if [ ! -f "$$SDK/licenses/android-sdk-license" ]; then \
	  echo 'Set up the Android SDK with: android --sdk="$$HOME/Android/Sdk" sdk install platform-tools (see android/README.md)'; exit 1; \
	fi; \
	cd android && ./gradlew --quiet -PappVersion=$(VERSION)

apk:
	@$(ANDROID_GRADLE) :app:assembleDebug
	@mkdir -p bin
	cp android/app/build/outputs/apk/debug/app-debug.apk bin/$(APK)

android-check: apk
	@$(ANDROID_GRADLE) :app:testDebugUnitTest :app:lintDebug
	@echo "==> make android-check: all green"

# Explicit installs only; no build or check target installs anything.
install: apk
	adb install -r bin/$(APK)

install-host: build
	install -Dm755 bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	install -Dm644 host/deploy/desklinkd.service $(HOME)/.config/systemd/user/desklinkd.service
	systemctl --user daemon-reload
	@echo "Start it with: systemctl --user enable --now desklinkd"
	@echo "Fedora already grants the desktop user /dev/uinput; elsewhere install the udev rule:"
	@echo "  sudo install -m644 host/deploy/60-desklink.rules /etc/udev/rules.d/ && sudo udevadm trigger"

install-laptop: build
	install -Dm755 bin/$(CLIENT) $(HOME)/.local/bin/$(CLIENT)
	install -Dm644 host/deploy/com.desklink.Client.desktop $(HOME)/.local/share/applications/com.desklink.Client.desktop
	@echo "Video needs the GStreamer GTK sink: sudo dnf install gstreamer1-plugin-gtk4"

standards:
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.universal.md -o AGENTS.universal.md
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.go.md -o AGENTS.go.md
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.android.md -o AGENTS.android.md
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/headless-gui.sh -o scripts/headless-gui.sh && chmod +x scripts/headless-gui.sh

clean:
	rm -rf bin/

.PHONY: test vet fmt lint agents-size build smoke check apk android-check install install-host install-laptop standards clean
