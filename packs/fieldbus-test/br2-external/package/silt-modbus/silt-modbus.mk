################################################################################
#
# silt-modbus
#
################################################################################

SILT_MODBUS_VERSION = 1.0
SILT_MODBUS_SITE = $(SILT_MODBUS_PKGDIR)
SILT_MODBUS_SITE_METHOD = local
SILT_MODBUS_LICENSE = MIT
SILT_MODBUS_DEPENDENCIES = silt-sim libmodbus

define SILT_MODBUS_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/silt-modbusd.c -o $(@D)/silt-modbusd -lsiltsim -lmodbus -lm
endef

define SILT_MODBUS_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/silt-modbusd $(TARGET_DIR)/usr/bin/silt-modbusd
endef

define SILT_MODBUS_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(SILT_MODBUS_PKGDIR)/S91silt-modbus \
		$(TARGET_DIR)/etc/init.d/S91silt-modbus
endef

define SILT_MODBUS_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(SILT_MODBUS_PKGDIR)/silt-modbus.service \
		$(TARGET_DIR)/usr/lib/systemd/system/silt-modbus.service
endef

$(eval $(generic-package))
