BINARY  := bible
LDFLAGS := -s -w
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

export CGO_ENABLED := 0

.PHONY: build test parity release clean

build: data/bible.db
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

release: data/bible.db
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building dist/$(BINARY)-$$os-$$arch$$ext"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" \
			-o dist/$(BINARY)-$$os-$$arch$$ext . || exit 1; \
	done

data/bible.db:
	@echo "data/bible.db is missing: copy your bible.db there first." >&2
	@exit 1

clean:
	rm -rf $(BINARY) dist
