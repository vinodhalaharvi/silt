.PHONY: tidy build vet test check fmt silt ci ci-fast ci-kbuild ci-linux

silt:
	go build -o bin/silt ./cmd/silt

tidy:
	go mod tidy

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

fmt:
	gofmt -l -w .

check: vet test
	@gofmt -l . | grep . && { echo "unformatted files above"; exit 1; } || echo "gofmt clean"

# The same checks CI runs, in the same order, so a red CI is something you
# already saw. This used to stop after check-images and skipped the two steps
# that then failed in CI for a week: the kbuild experiments, which run every
# imported board and every image through Buildroot's own conf, and the Linux
# checks. Five minutes here against twenty in CI, on a machine that already has
# the trees.
#
#   make ci BUILDROOT=~/buildroot
#   make ci BUILDROOT=~/buildroot LINUX=~/linux      # also the kernel half
#
# ci-fast leaves out the kbuild experiments, which are most of the five
# minutes, for when the change cannot touch a defconfig.
ci: ci-fast ci-kbuild ci-linux

ci-fast: check silt
	@test -n "$(BUILDROOT)" || { echo "set BUILDROOT=<buildroot 2025.02.16 checkout>"; exit 1; }
	SILT_BUILDROOT="$(BUILDROOT)" go test ./...
	ci/check-images.sh "$(BUILDROOT)" bin/silt $(LINUX)

ci-kbuild:
	@test -n "$(BUILDROOT)" || { echo "set BUILDROOT=<buildroot 2025.02.16 checkout>"; exit 1; }
	SILT_BUILDROOT="$(BUILDROOT)" SILT_KBUILD=1 go test ./importer ./fixpoint -timeout 30m \
	  -run 'TestFactoring|TestImportedTargetsTakeLibraryProfiles|TestModelAcceptsEveryDefconfig|TestLibraryImagesAgainstKbuild'

# Skipped, loudly, when LINUX is unset: a silent skip is how a check stops
# being run without anyone deciding that.
ci-linux:
	@if [ -z "$(LINUX)" ]; then \
	  echo "ci-linux: skipped, no LINUX=<kernel source> given"; \
	else \
	  SILT_LINUX="$(LINUX)" go test ./verify -run Linux && \
	  SILT_LINUX="$(LINUX)" SILT_KBUILD=1 go test ./kconfig -run KernelConf; \
	fi
