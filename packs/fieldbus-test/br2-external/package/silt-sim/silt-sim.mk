################################################################################
#
# silt-sim
#
################################################################################

SILT_SIM_VERSION = 1.0
SILT_SIM_SITE = $(SILT_SIM_PKGDIR)
SILT_SIM_SITE_METHOD = local
SILT_SIM_LICENSE = MIT
SILT_SIM_DEPENDENCIES = cjson
SILT_SIM_INSTALL_STAGING = YES

define SILT_SIM_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) -fPIC -shared \
		$(@D)/siltsim.c -o $(@D)/libsiltsim.so -lcjson -lm
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/silt-simd.c -o $(@D)/silt-simd -L$(@D) -lsiltsim -lm
endef

# The protocol daemons compile against the same header and library, which is
# what keeps one definition of the table shared by all of them.
define SILT_SIM_INSTALL_STAGING_CMDS
	$(INSTALL) -D -m 0644 $(@D)/siltsim.h $(STAGING_DIR)/usr/include/siltsim.h
	$(INSTALL) -D -m 0755 $(@D)/libsiltsim.so $(STAGING_DIR)/usr/lib/libsiltsim.so
endef

define SILT_SIM_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/libsiltsim.so $(TARGET_DIR)/usr/lib/libsiltsim.so
	$(INSTALL) -D -m 0755 $(@D)/silt-simd $(TARGET_DIR)/usr/bin/silt-simd
	$(INSTALL) -D -m 0644 $(SILT_SIM_PKGDIR)/silt-sim.json \
		$(TARGET_DIR)/etc/silt-sim.json
endef

define SILT_SIM_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(SILT_SIM_PKGDIR)/S88silt-sim \
		$(TARGET_DIR)/etc/init.d/S88silt-sim
endef

define SILT_SIM_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(SILT_SIM_PKGDIR)/silt-sim.service \
		$(TARGET_DIR)/usr/lib/systemd/system/silt-sim.service
endef

$(eval $(generic-package))
