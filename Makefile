# qisqa build tasks.
#
#   make build                 build ./qisqa for this machine
#   make test                  run the tests
#   make dist VERSION=1.2.3    build the release archives into dist/
#   make clean                 remove everything the other targets created

VERSION   ?= dev
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# A static binary without debug tables or local paths, stamped with the version.
BUILD := CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test dist clean

build:
	$(BUILD) -o qisqa .

test:
	go test -race ./...

# One archive per platform, each holding the binary, README.md, LICENSE,
# config.example.yaml and the pages of docs, so that the README's links
# work in it, plus checksums.txt with the SHA-256 of every archive.
dist: clean
	@set -e; for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		name=qisqa_$(VERSION)_$${os}_$${arch}; \
		echo "building $$name"; \
		mkdir -p dist/$$name/docs; \
		GOOS=$$os GOARCH=$$arch $(BUILD) -o dist/$$name/qisqa .; \
		cp README.md LICENSE config.example.yaml dist/$$name/; \
		cp docs/*.md dist/$$name/docs/; \
		tar -czf dist/$$name.tar.gz -C dist/$$name qisqa README.md LICENSE config.example.yaml docs; \
		rm -r dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz > checksums.txt

clean:
	rm -rf dist qisqa
