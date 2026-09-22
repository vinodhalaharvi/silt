################################################################################
#
# silt-opcua
#
################################################################################

SILT_OPCUA_VERSION = 1.0
SILT_OPCUA_SITE = $(SILT_OPCUA_PKGDIR)
SILT_OPCUA_SITE_METHOD = local
SILT_OPCUA_LICENSE = MIT
SILT_OPCUA_DEPENDENCIES = silt-sim open62541

define SILT_OPCUA_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/silt-opcuad.c -o $(@D)/silt-opcuad -lsiltsim -lopen62541 -lm
endef

define SILT_OPCUA_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/silt-opcuad $(TARGET_DIR)/usr/bin/silt-opcuad
endef

define SILT_OPCUA_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(SILT_OPCUA_PKGDIR)/S90silt-opcua \
		$(TARGET_DIR)/etc/init.d/S90silt-opcua
endef

define SILT_OPCUA_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(SILT_OPCUA_PKGDIR)/silt-opcua.service \
		$(TARGET_DIR)/usr/lib/systemd/system/silt-opcua.service
endef

$(eval $(generic-package))
