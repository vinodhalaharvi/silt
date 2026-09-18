################################################################################
#
# hello-silt
#
################################################################################

HELLO_SILT_VERSION = 1.0
HELLO_SILT_SITE = $(HELLO_SILT_PKGDIR)
HELLO_SILT_SITE_METHOD = local
HELLO_SILT_LICENSE = MIT

define HELLO_SILT_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/hello.c -o $(@D)/hello-silt
endef

define HELLO_SILT_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/hello-silt $(TARGET_DIR)/usr/bin/hello-silt
endef

$(eval $(generic-package))
