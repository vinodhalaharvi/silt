################################################################################
#
# silt-can
#
################################################################################

SILT_CAN_VERSION = 1.0
SILT_CAN_SITE = $(SILT_CAN_PKGDIR)
SILT_CAN_SITE_METHOD = local
SILT_CAN_LICENSE = MIT
SILT_CAN_DEPENDENCIES = silt-sim

define SILT_CAN_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/silt-cand.c -o $(@D)/silt-cand -lsiltsim -lm
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) -DSILTCAN_NO_MAIN \
		$(@D)/silt-cand-test.c $(@D)/silt-cand.c \
		-o $(@D)/silt-cand-test -lsiltsim -lm
endef

# The frame packing is the part with no socket in it, so it is tested on the
# target rather than mocked: ci and a buyer can both run silt-cand-test.
define SILT_CAN_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/silt-cand $(TARGET_DIR)/usr/bin/silt-cand
	$(INSTALL) -D -m 0755 $(@D)/silt-cand-test $(TARGET_DIR)/usr/bin/silt-cand-test
endef

define SILT_CAN_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(SILT_CAN_PKGDIR)/S92silt-can \
		$(TARGET_DIR)/etc/init.d/S92silt-can
endef

define SILT_CAN_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(SILT_CAN_PKGDIR)/silt-can.service \
		$(TARGET_DIR)/usr/lib/systemd/system/silt-can.service
endef

$(eval $(generic-package))
