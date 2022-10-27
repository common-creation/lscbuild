.PHONY: build

build:
	env CGO_ENABLED=0 go build -ldflags '-s -w -s -w -X main.Version=nil' ./cmd/lscbuild

clean:
	rm -f ./lscbuild
