################################################################################
#
# k3s
#
# The upstream release binary, installed as is. k3s is a Go program that
# vendors containerd, runc, flannel and CoreDNS; building it from source needs
# a Go toolchain, a large build cache and roughly an hour, and produces the
# same binary upstream publishes. This package takes the published one, which
# is why it has a per-architecture hash rather than a version control commit.
#
################################################################################

K3S_VERSION = v1.31.14+k3s1
K3S_SITE = https://github.com/k3s-io/k3s/releases/download/$(subst +,%2B,$(K3S_VERSION))
K3S_LICENSE = Apache-2.0

ifeq ($(BR2_aarch64),y)
K3S_BIN = k3s-arm64
else
K3S_BIN = k3s
endif

K3S_SOURCE = $(K3S_BIN)

# The binary is not an archive, so there is nothing to extract.
define K3S_EXTRACT_CMDS
	cp $(K3S_DL_DIR)/$(K3S_BIN) $(@D)/k3s
endef

define K3S_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/k3s $(TARGET_DIR)/usr/bin/k3s
	ln -sf k3s $(TARGET_DIR)/usr/bin/kubectl
	ln -sf k3s $(TARGET_DIR)/usr/bin/crictl
	ln -sf k3s $(TARGET_DIR)/usr/bin/ctr
endef

define K3S_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(K3S_PKGDIR)/k3s.service \
		$(TARGET_DIR)/usr/lib/systemd/system/k3s.service
endef

$(eval $(generic-package))
