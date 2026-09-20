################################################################################
#
# agent-gateway-host
#
################################################################################

AGENT_GATEWAY_HOST_VERSION = 0.1.0
AGENT_GATEWAY_HOST_SITE = https://github.com/vinodhalaharvi/silt-releases/releases/download/agent-gateway-host-v$(AGENT_GATEWAY_HOST_VERSION)
AGENT_GATEWAY_HOST_SOURCE = agent-gateway-host-$(AGENT_GATEWAY_HOST_VERSION)-aarch64.tar.xz
AGENT_GATEWAY_HOST_LICENSE = Apache-2.0
AGENT_GATEWAY_HOST_STRIP_COMPONENTS = 0

# A static musl binary: it runs on this glibc image without carrying a
# second C library's worth of assumptions about it, and what the appliance
# runs is one file whose hash is the one release.sh printed.
define AGENT_GATEWAY_HOST_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/agent-gateway-host $(TARGET_DIR)/usr/bin/agent-gateway-host
endef

$(eval $(generic-package))
