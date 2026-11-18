# qisqa build tasks.
#
#   make build                 build ./qisqa for this machine
#   make test                  run the tests
#   make dist VERSION=1.2.3    build the release archives into dist/
#   make zones                 make the list of domain zones again
#   make clean                 remove everything the other targets created

VERSION   ?= dev
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# A static binary without debug tables or local paths, stamped with the version.
BUILD := CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test dist zones clean

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

# internal/app/zones.txt: every domain zone of IANA, ordered by how many sites
# of the Tranco list of the million most visited sites it holds, the zones
# without a site last, by name. Text mode finds URLs without a scheme by it.
# Needs curl and awk.
zones:
	@set -e; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	curl -fsSL -o "$$tmp/iana.txt" https://data.iana.org/TLD/tlds-alpha-by-domain.txt; \
	id=$$(curl -fsSL https://tranco-list.eu/top-1m-id); \
	curl -fsSL -o "$$tmp/sites.csv" "https://tranco-list.eu/download/$$id/1000000"; \
	{ \
		echo "# The domain zones of IANA, ordered by how many sites of the Tranco list of"; \
		echo "# the million most visited sites each one holds; the zones without a site"; \
		echo "# come last, by name. Made by: make zones."; \
		echo "# IANA list: $$(sed -n '1s/^# //p' "$$tmp/iana.txt")"; \
		echo "# Tranco list: $$id, $$(wc -l <"$$tmp/sites.csv" | tr -d ' ') sites"; \
		awk 'FNR == NR { if ($$0 !~ /^#/ && $$0 != "") sites[tolower($$0)] = 0; next } \
			{ sub(/\r$$/, ""); n = split($$0, label, "."); zone = tolower(label[n]); if (zone in sites) sites[zone]++ } \
			END { for (zone in sites) print sites[zone], zone }' "$$tmp/iana.txt" "$$tmp/sites.csv" | \
			LC_ALL=C sort -k1,1nr -k2,2 | cut -d' ' -f2; \
	} >internal/app/zones.txt; \
	echo "internal/app/zones.txt: $$(grep -vc '^#' internal/app/zones.txt) zones"

clean:
	rm -rf dist qisqa
