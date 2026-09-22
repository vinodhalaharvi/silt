################################################################################
#
# silt-http
#
################################################################################

SILT_HTTP_VERSION = 1.0
SILT_HTTP_SITE = $(SILT_HTTP_PKGDIR)
SILT_HTTP_SITE_METHOD = local
SILT_HTTP_LICENSE = MIT
SILT_HTTP_DEPENDENCIES = silt-sim libmicrohttpd

define SILT_HTTP_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/silt-httpd.c -o $(@D)/silt-httpd -lsiltsim -lmicrohttpd -lm
endef

define SILT_HTTP_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/silt-httpd $(TARGET_DIR)/usr/bin/silt-httpd
endef

define SILT_HTTP_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(SILT_HTTP_PKGDIR)/S93silt-http \
		$(TARGET_DIR)/etc/init.d/S93silt-http
endef

define SILT_HTTP_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(SILT_HTTP_PKGDIR)/silt-http.service \
		$(TARGET_DIR)/usr/lib/systemd/system/silt-http.service
endef

$(eval $(generic-package))
