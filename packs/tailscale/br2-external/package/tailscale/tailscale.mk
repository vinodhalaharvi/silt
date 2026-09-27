################################################################################
#
# tailscale
#
################################################################################

TAILSCALE_VERSION = 1.80.3
TAILSCALE_SITE = $(call github,tailscale,tailscale,v$(TAILSCALE_VERSION))
TAILSCALE_LICENSE = BSD-3-Clause
TAILSCALE_LICENSE_FILES = LICENSE
TAILSCALE_DEPENDENCIES = ca-certificates

# Tailscale does not vendor its dependencies, so Buildroot runs go mod vendor
# during the download step and repacks the tarball. The hash in
# tailscale.hash is therefore the hash of the *vendored* tarball, which
# cannot be taken from upstream: see the note in that file for how to get it
# the first time.
TAILSCALE_GOMOD = tailscale.com

TAILSCALE_BUILD_TARGETS = cmd/tailscaled cmd/tailscale
TAILSCALE_INSTALL_BINS = tailscaled tailscale

TAILSCALE_LDFLAGS = \
	-X tailscale.com/version.longStamp=$(TAILSCALE_VERSION) \
	-X tailscale.com/version.shortStamp=$(TAILSCALE_VERSION)

define TAILSCALE_INSTALL_STATE_DIR
	$(INSTALL) -d -m 0700 $(TARGET_DIR)/var/lib/tailscale
endef
TAILSCALE_POST_INSTALL_TARGET_HOOKS += TAILSCALE_INSTALL_STATE_DIR

define TAILSCALE_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(TAILSCALE_PKGDIR)/S41tailscale \
		$(TARGET_DIR)/etc/init.d/S41tailscale
	$(INSTALL) -D -m 0644 $(TAILSCALE_PKGDIR)/tailscale.default \
		$(TARGET_DIR)/etc/default/tailscale
endef

define TAILSCALE_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(TAILSCALE_PKGDIR)/tailscaled.service \
		$(TARGET_DIR)/usr/lib/systemd/system/tailscaled.service
	$(INSTALL) -D -m 0644 $(TAILSCALE_PKGDIR)/tailscale.default \
		$(TARGET_DIR)/etc/default/tailscale
endef

$(eval $(golang-package))
