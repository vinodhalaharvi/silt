################################################################################
#
# hello-opcua
#
################################################################################

HELLO_OPCUA_VERSION = 1.0
HELLO_OPCUA_SITE = $(HELLO_OPCUA_PKGDIR)
HELLO_OPCUA_SITE_METHOD = local
HELLO_OPCUA_LICENSE = MIT
HELLO_OPCUA_DEPENDENCIES = open62541

define HELLO_OPCUA_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/hello-opcua.c -o $(@D)/hello-opcua -lopen62541
endef

define HELLO_OPCUA_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/hello-opcua $(TARGET_DIR)/usr/bin/hello-opcua
endef

define HELLO_OPCUA_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(HELLO_OPCUA_PKGDIR)/S90hello-opcua \
		$(TARGET_DIR)/etc/init.d/S90hello-opcua
endef

define HELLO_OPCUA_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(HELLO_OPCUA_PKGDIR)/hello-opcua.service \
		$(TARGET_DIR)/usr/lib/systemd/system/hello-opcua.service
endef

$(eval $(generic-package))
