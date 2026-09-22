################################################################################
#
# silt-mqtt
#
################################################################################

SILT_MQTT_VERSION = 1.0
SILT_MQTT_SITE = $(SILT_MQTT_PKGDIR)
SILT_MQTT_SITE_METHOD = local
SILT_MQTT_LICENSE = MIT
SILT_MQTT_DEPENDENCIES = silt-sim paho-mqtt-c

define SILT_MQTT_BUILD_CMDS
	$(TARGET_CC) $(TARGET_CFLAGS) $(TARGET_LDFLAGS) \
		$(@D)/silt-mqttd.c -o $(@D)/silt-mqttd -lsiltsim -lpaho-mqtt3c -lm
endef

# mosquitto 2.0 listens on localhost only unless a listener is configured, so
# a subscriber on a laptop would be refused without this file.
define SILT_MQTT_INSTALL_TARGET_CMDS
	$(INSTALL) -D -m 0755 $(@D)/silt-mqttd $(TARGET_DIR)/usr/bin/silt-mqttd
	$(INSTALL) -D -m 0644 $(SILT_MQTT_PKGDIR)/mosquitto.conf \
		$(TARGET_DIR)/etc/mosquitto/mosquitto.conf
endef

define SILT_MQTT_INSTALL_INIT_SYSV
	$(INSTALL) -D -m 0755 $(SILT_MQTT_PKGDIR)/S94silt-mqtt \
		$(TARGET_DIR)/etc/init.d/S94silt-mqtt
endef

define SILT_MQTT_INSTALL_INIT_SYSTEMD
	$(INSTALL) -D -m 0644 $(SILT_MQTT_PKGDIR)/silt-mqtt.service \
		$(TARGET_DIR)/usr/lib/systemd/system/silt-mqtt.service
endef

$(eval $(generic-package))
