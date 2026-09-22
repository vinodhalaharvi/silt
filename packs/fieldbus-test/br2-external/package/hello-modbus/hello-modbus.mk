################################################################################
#
# hello-modbus
#
################################################################################

HELLO_MODBUS_VERSION = 1.0
HELLO_MODBUS_SITE = $(HELLO_MODBUS_PKGDIR)
HELLO_MODBUS_SITE_METHOD = local
HELLO_MODBUS_LICENSE = MIT
HELLO_MODBUS_DEPENDENCIES = libmodbus

define HELLO_MODBUS_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/hello-modbus.c -o $(@D)/hello-modbus -lmodbus
endef

define HELLO_MODBUS_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/hello-modbus $(TARGET_DIR)/usr/bin/hello-modbus
endef

define HELLO_MODBUS_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(HELLO_MODBUS_PKGDIR)/S91hello-modbus \
		$(TARGET_DIR)/etc/init.d/S91hello-modbus
endef

define HELLO_MODBUS_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(HELLO_MODBUS_PKGDIR)/hello-modbus.service \
		$(TARGET_DIR)/usr/lib/systemd/system/hello-modbus.service
endef

$(eval $(generic-package))
