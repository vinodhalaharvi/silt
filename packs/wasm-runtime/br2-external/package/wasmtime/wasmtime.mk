################################################################################
#
# wasmtime
#
# The upstream release binary, installed as is. Both binaries in the release
# are dynamically linked against glibc (ld-linux-aarch64.so.1, libc, libm,
# libpthread, libgcc_s, libdl), which is why the package depends on a glibc
# toolchain: on a musl rootfs it would install and fail to start.
#
################################################################################

WASMTIME_VERSION = v46.0.3
WASMTIME_SITE = https://github.com/bytecodealliance/wasmtime/releases/download/$(WASMTIME_VERSION)
WASMTIME_LICENSE = Apache-2.0 WITH LLVM-exception
WASMTIME_LICENSE_FILES = LICENSE

ifeq ($(BR2_aarch64),y)
WASMTIME_ARCH = aarch64
else
WASMTIME_ARCH = x86_64
endif

WASMTIME_SOURCE = wasmtime-$(WASMTIME_VERSION)-$(WASMTIME_ARCH)-linux.tar.xz
WASMTIME_STRIP_COMPONENTS = 1

ifeq ($(BR2_PACKAGE_WASMTIME_FULL),y)
WASMTIME_BIN = wasmtime
else
WASMTIME_BIN = wasmtime-min
endif

# Both builds install as /usr/bin/wasmtime: which one is in the image is a
# build decision, and nothing on the appliance should have to know which.
define WASMTIME_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/$(WASMTIME_BIN) $(TARGET_DIR)/usr/bin/wasmtime
endef

$(eval $(generic-package))
