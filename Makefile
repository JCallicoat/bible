BINARY  := bible
LDFLAGS := -s -w
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

# Embeddings endpoint for "make paragraphs" (any OpenAI-compatible
# /v1/embeddings server, e.g. llama-server --embeddings).
EMBED_URL     ?= http://127.0.0.1:8080
EMBED_MODEL   ?=
EMBED_API_KEY ?=
EMBED_FLAGS   ?=

export CGO_ENABLED := 0

.PHONY: build build-paragraphs paragraphs test release clean

build: data/bible.db
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

# Tests use a synthetic database (internal/testdb), not data/bible.db.
# The race detector needs cgo, so it is enabled just for this target.
test:
	CGO_ENABLED=1 go test -race ./internal/...

release: data/bible.db
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building dist/$(BINARY)-$$os-$$arch$$ext"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o dist/$(BINARY)-$$os-$$arch$$ext . || exit 1; \
	done

# Optional: (re)generate the paragraphs table in data/bible.db from KJV
# embeddings. embed-paragraphs vacuums the database when it is done.
paragraphs: data/bible.db
	go run . embed-paragraphs --db data/bible.db --embed-url "$(EMBED_URL)" \
		--embed-model "$(EMBED_MODEL)" --embed-api-key "$(EMBED_API_KEY)" $(EMBED_FLAGS)

build-paragraphs:
	$(MAKE) paragraphs
	$(MAKE) build

data/bible.db:
	@echo "data/bible.db is missing: copy your bible.db there first." >&2
	@exit 1

clean:
	rm -rf $(BINARY) dist
