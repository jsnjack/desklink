BINARY  := desklinkd
CLIENT  := desklink
APK     := desklink.apk
REPO    := jsnjack/desklink
# The personal app key: the debug keystore that local builds and every
# released APK are signed with, so either installs over the other.
KEYSTORE ?= $(HOME)/.android/debug.keystore
# SHA-256 of the app key's certificate. Every machine that releases needs the
# keystore holding it: an APK signed with another key cannot update the
# installed app ("App not installed"), so release-apk refuses it.
APP_CERT_SHA256 := 53d59cccec238bea3efb034a95734a69134d15b220330c2bab7ebedbb6613ebf
RELEASE_APK := android/app/build/outputs/apk/release/app-release-unsigned.apk
# Release artifacts (RPMs and the APK) land here.
DIST    := dist
# monova fails until the first commit exists; 0.0.0 keeps local builds working.
VERSION := $(shell monova 2>/dev/null || echo 0.0.0)
LDFLAGS := -ldflags="-X main.Version=$(VERSION)"
# desklinkd exports its pw_stream_dequeue_buffer, so the pipewire plugin calls
# it and desklinkd reads the pointer apart from the video
# (host/internal/video/cursorhook.c). Passed to the external linker here,
# since cgo refuses the flag in #cgo LDFLAGS.
DAEMON_LDFLAGS := -ldflags="-X main.Version=$(VERSION) -extldflags=-Wl,--export-dynamic-symbol=pw_stream_dequeue_buffer"

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
	cd host && CGO_ENABLED=1 go build $(DAEMON_LDFLAGS) -o ../bin/$(BINARY) ./cmd/desklinkd
	@nm -D --defined-only bin/$(BINARY) | grep -qw pw_stream_dequeue_buffer || \
	  { echo "bin/$(BINARY) does not export pw_stream_dequeue_buffer: clients would get the pointer in the video"; exit 1; }
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

# screenshots renders the app's screens on the JVM (Robolectric and
# Roborazzi, made-up state, Galaxy Tab S9 size) into bin/screenshots to look
# at; android-check leaves these tests out and nothing compares the images.
SCREENSHOTS := $(CURDIR)/bin/screenshots
screenshots:
	@rm -rf $(SCREENSHOTS) && mkdir -p $(SCREENSHOTS)
	@$(ANDROID_GRADLE) :app:testDebugUnitTest -Pscreenshots=$(SCREENSHOTS)
	@echo "==> screenshots in $(SCREENSHOTS)"

# release-apk builds the optimized, non-debuggable APK that releases publish
# and signs it with the app key. The debug APK from make apk stays for
# development.
release-apk:
	@$(ANDROID_GRADLE) :app:assembleRelease
	@mkdir -p bin/release
	set -e; \
	SDK=$${ANDROID_HOME:-$$(sed -n 's/^sdk.dir=//p' android/local.properties 2>/dev/null)}; \
	SIGNER=$$SDK/build-tools/37.0.0/apksigner; \
	"$$SIGNER" sign --ks $(KEYSTORE) --ks-pass pass:android --ks-key-alias androiddebugkey \
	  --out bin/release/$(APK) $(RELEASE_APK); \
	"$$SIGNER" verify bin/release/$(APK); \
	CERT=$$("$$SIGNER" verify --print-certs bin/release/$(APK) 2>/dev/null | sed -n 's/.*certificate SHA-256 digest: //p' | head -1); \
	if [ "$$CERT" != "$(APP_CERT_SHA256)" ]; then \
	  rm -f bin/release/$(APK) bin/release/$(APK).idsig; \
	  echo "$(KEYSTORE) signs with certificate $$CERT, not the app key $(APP_CERT_SHA256)."; \
	  echo "Copy the keystore holding the app key from the machine that signed earlier releases to $(KEYSTORE)."; \
	  exit 1; \
	fi

# Explicit installs only; no build or check target installs anything.
install: apk
	adb install -r bin/$(APK)

install-host: build
	install -Dm755 bin/$(BINARY) $(HOME)/.local/bin/$(BINARY)
	install -Dm644 host/deploy/desklinkd.service $(HOME)/.config/systemd/user/desklinkd.service
	systemctl --user daemon-reload
	@echo "Start it with: systemctl --user enable --now desklinkd"
	@echo "Input injection needs /dev/uinput; unless the desklinkd RPM is installed, run:"
	@echo "  sudo install -m644 host/deploy/60-desklink.rules /etc/udev/rules.d/ && sudo udevadm trigger --sysname-match=uinput"

# rpm packages the binaries built above; rpmbuild does not compile Go here.
# One spec gives two RPMs: desklinkd (host, with the systemd user unit and the
# udev rule) and desklink (laptop client). See packaging/desklink.spec.
rpm: build
	@command -v rpmbuild >/dev/null 2>&1 || { echo "Install rpmbuild with: sudo dnf install rpm-build systemd-rpm-macros"; exit 1; }
	rm -rf rpmbuild/RPMS
	rpmbuild -bb --define "_topdir $(CURDIR)/rpmbuild" \
	    --define "srcdir $(CURDIR)" \
	    --define "appversion $(VERSION)" packaging/desklink.spec
	@echo "==> RPMs:"; find rpmbuild/RPMS -name '*.rpm'

# release-artifacts collects what a release publishes in dist/: both RPMs and
# the signed release APK. The APK keeps a fixed name, so the latest release's
# download link stays the same.
release-artifacts: rpm release-apk
	rm -rf $(DIST)
	mkdir -p $(DIST)
	find rpmbuild/RPMS -name '*-$(VERSION)-*.rpm' -exec cp {} $(DIST)/ \;
	cp bin/release/$(APK) $(DIST)/$(APK)
	@echo "==> release artifacts in $(DIST)/:"; ls -1 $(DIST)

# release publishes the pushed master commit: it checks the tree, runs both
# gates, builds the artifacts and uploads them to the GitHub release
# v<monova version> with grm, which creates the release or resumes a partial
# upload (grm authenticates with its own token, grm set token …).
release-preflight:
	@command -v grm >/dev/null 2>&1 || { echo "Install grm from https://github.com/jsnjack/grm"; exit 1; }
	@command -v monova >/dev/null 2>&1 || { echo "Install monova with: grm install jsnjack/monova"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "Commit your changes and push master before make release."; exit 1; }
	@test "$$(git branch --show-current)" = master || { echo "Switch to master before make release."; exit 1; }
	@test "$$(git rev-parse HEAD)" = "$$(git ls-remote origin refs/heads/master | cut -f1)" || { echo "Push this commit to origin/master before make release."; exit 1; }

release: release-preflight check android-check
	$(MAKE) release-preflight
	$(MAKE) release-artifacts
	@args=""; for f in $(DIST)/*; do args="$$args -f $$f"; done; \
	  echo "==> grm release $(REPO)$$args -t v$(VERSION)"; \
	  grm release $(REPO) $$args -t "v$(VERSION)"
	@echo "==> released v$(VERSION): https://github.com/$(REPO)/releases/tag/v$(VERSION)"
	$(MAKE) publish-apk

# The install page https://jsnjack.github.io/android-apps/ offers every app's
# latest APK from the "apps" release of jsnjack/android-apps.
INSTALL_PAGE := jsnjack/android-apps

# publish-apk puts the signed release APK on the install page, replacing the
# previous one, labelled with the version the page shows. make release runs
# it last; run it again to retry.
publish-apk: release-preflight release-apk
	@command -v gh >/dev/null 2>&1 || { echo "Install GitHub CLI with: sudo dnf install gh"; exit 1; }
	@gh auth status --hostname github.com >/dev/null
	gh release upload apps "bin/release/$(APK)#$$(monova)" --clobber --repo $(INSTALL_PAGE)
	@echo "==> on the install page: https://jsnjack.github.io/android-apps/"

install-laptop: build
	install -Dm755 bin/$(CLIENT) $(HOME)/.local/bin/$(CLIENT)
	install -Dm644 host/deploy/com.desklink.Client.desktop $(HOME)/.local/share/applications/com.desklink.Client.desktop
	install -Dm644 host/deploy/com.desklink.Client.svg $(HOME)/.local/share/icons/hicolor/scalable/apps/com.desklink.Client.svg
	install -Dm644 host/deploy/com.desklink.Client-symbolic.svg $(HOME)/.local/share/icons/hicolor/symbolic/apps/com.desklink.Client-symbolic.svg
	@echo "Video needs the GStreamer GTK sink: sudo dnf install gstreamer1-plugin-gtk4"

standards:
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.universal.md -o AGENTS.universal.md
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.go.md -o AGENTS.go.md
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/AGENTS.android.md -o AGENTS.android.md
	curl -fsSL https://raw.githubusercontent.com/jsnjack/standards/master/headless-gui.sh -o scripts/headless-gui.sh && chmod +x scripts/headless-gui.sh

clean:
	rm -rf bin/ rpmbuild/ $(DIST)

.NOTPARALLEL: release

.PHONY: test vet fmt lint agents-size build smoke check apk android-check screenshots release-apk install install-host install-laptop rpm release-artifacts release-preflight release standards publish-apk clean
