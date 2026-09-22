/*
 * silt-mqttd - the device on MQTT.
 *
 * Publishes every tag to a local broker as it changes, and accepts writes
 * back on a companion topic:
 *
 *   silt/<device>/<tag>        retained, the value, e.g. 54.3
 *   silt/<device>/<tag>/set    write a writable tag, e.g. 72.5
 *   silt/<device>/state        retained, every tag as one JSON object
 *
 * This is the direction a real gateway runs: the field protocols are
 * southbound, MQTT is northbound, for dashboards and whatever else is
 * subscribed. Nothing inside this device talks over MQTT - the protocols
 * share the table in /dev/shm - so the broker being down costs the bridge
 * and nothing else.
 *
 * Published retained, so a dashboard that connects later gets the current
 * value at once instead of waiting for the next change. Only changes are
 * published; a value that has not moved is not news.
 */

#include "siltsim.h"

#include <MQTTClient.h>

#include <math.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define TICK_MS      200
#define STATE_MS     5000
#define TOPIC_LEN    (16 + SILTSIM_NAME_LEN * 2)
#define CLIENT_ID    "silt-mqttd"

static volatile sig_atomic_t running = 1;
static struct siltsim_table *table;

static void on_stop(int sig) { (void)sig; running = 0; }

static void format(const struct siltsim_tag *t, double v, char *buf, size_t cap)
{
	if (t->type == SILTSIM_BOOL)
		snprintf(buf, cap, "%s", v != 0 ? "true" : "false");
	else if (t->type == SILTSIM_INT)
		snprintf(buf, cap, "%lld", (long long)v);
	else
		snprintf(buf, cap, "%.4g", v);
}

static void publish(MQTTClient client, const char *topic, const char *payload)
{
	MQTTClient_message m = MQTTClient_message_initializer;

	m.payload = (void *)payload;
	m.payloadlen = (int)strlen(payload);
	m.qos = 0;
	m.retained = 1;
	MQTTClient_publishMessage(client, topic, &m, NULL);
}

/* A write from MQTT reaches writable tags only, so the sim core stays the
 * only writer of simulated ones. */
static int on_message(void *ctx, char *topic, int topic_len,
		      MQTTClient_message *m)
{
	char name[SILTSIM_NAME_LEN] = "", payload[64] = "";
	uint32_t i;

	(void)ctx; (void)topic_len;

	/* silt/<device>/<tag>/set */
	{
		const char *p = strrchr(topic, '/');
		const char *q;

		if (!p || strcmp(p, "/set") != 0)
			goto done;
		for (q = p - 1; q > topic && *q != '/'; q--)
			;
		snprintf(name, sizeof(name), "%.*s", (int)(p - q - 1), q + 1);
	}

	if ((size_t)m->payloadlen >= sizeof(payload))
		goto done;
	memcpy(payload, m->payload, (size_t)m->payloadlen);

	for (i = 0; i < table->tag_count; i++) {
		struct siltsim_tag *t = &table->tag[i];
		double v;

		if (strcmp(t->name, name) != 0)
			continue;
		if (!t->writable) {
			fprintf(stderr, "silt-mqttd: \"%s\" is not writable\n", name);
			break;
		}
		if (!strcmp(payload, "true"))
			v = 1;
		else if (!strcmp(payload, "false"))
			v = 0;
		else
			v = strtod(payload, NULL);
		siltsim_set(t, v);
		break;
	}

done:
	MQTTClient_freeMessage(&m);
	MQTTClient_free(topic);
	return 1;
}

static void publish_state(MQTTClient client)
{
	char topic[TOPIC_LEN], body[4096];
	size_t at = 0;
	uint32_t i;

	at += (size_t)snprintf(body + at, sizeof(body) - at, "{");
	for (i = 0; i < table->tag_count && at < sizeof(body); i++) {
		const struct siltsim_tag *t = &table->tag[i];
		char v[64];

		format(t, siltsim_get(t), v, sizeof(v));
		at += (size_t)snprintf(body + at, sizeof(body) - at, "%s\"%s\":%s",
				       i ? "," : "", t->name, v);
	}
	if (at < sizeof(body))
		snprintf(body + at, sizeof(body) - at, "}");

	snprintf(topic, sizeof(topic), "silt/%s/state", table->device);
	publish(client, topic, body);
}

int main(int argc, char **argv)
{
	static double published[SILTSIM_MAX_TAGS];
	static int have_published[SILTSIM_MAX_TAGS];
	char err[SILTSIM_ERR_LEN] = "";
	const char *broker = "tcp://127.0.0.1:1883";
	MQTTClient_connectOptions opts = MQTTClient_connectOptions_initializer;
	uint32_t seen_generation;
	uint64_t next_state = 0;
	MQTTClient client;
	char sub[TOPIC_LEN];
	int opt;

	while ((opt = getopt(argc, argv, "b:h")) != -1) {
		if (opt == 'b') {
			broker = optarg;
		} else {
			fprintf(stderr, "usage: %s [-b tcp://host:port]\n", argv[0]);
			return opt == 'h' ? EXIT_SUCCESS : EXIT_FAILURE;
		}
	}

	signal(SIGINT, on_stop);
	signal(SIGTERM, on_stop);

	for (;;) {
		table = siltsim_attach(1, err, sizeof(err));
		if (table && table->tag_count > 0)
			break;
		fprintf(stderr, "silt-mqttd: waiting for the sim core: %s\n", err);
		sleep(2);
	}
	seen_generation = __atomic_load_n(&table->generation, __ATOMIC_ACQUIRE);

	MQTTClient_create(&client, broker, CLIENT_ID,
			  MQTTCLIENT_PERSISTENCE_NONE, NULL);
	MQTTClient_setCallbacks(client, NULL, NULL, on_message, NULL);
	opts.keepAliveInterval = 20;
	opts.cleansession = 1;

	/* The broker starts around the same time we do, and may be restarted
	 * later; keep trying rather than exiting. */
	while (running && MQTTClient_connect(client, &opts) != MQTTCLIENT_SUCCESS) {
		fprintf(stderr, "silt-mqttd: waiting for the broker at %s\n", broker);
		sleep(3);
	}
	if (!running)
		return EXIT_SUCCESS;

	snprintf(sub, sizeof(sub), "silt/%s/+/set", table->device);
	MQTTClient_subscribe(client, sub, 0);
	fprintf(stderr, "silt-mqttd: connected to %s, publishing silt/%s/#\n",
		broker, table->device);

	while (running) {
		uint64_t now = siltsim_now_ms();
		uint32_t i, gen;

		gen = __atomic_load_n(&table->generation, __ATOMIC_ACQUIRE);
		if (gen != seen_generation) {
			seen_generation = gen;
			memset(have_published, 0, sizeof(have_published));
			MQTTClient_subscribe(client, sub, 0);   /* same topic, new tags */
			fprintf(stderr, "silt-mqttd: reloaded, %u tag(s)\n",
				table->tag_count);
		}

		for (i = 0; i < table->tag_count; i++) {
			const struct siltsim_tag *t = &table->tag[i];
			char topic[TOPIC_LEN], value[64];
			double v = siltsim_get(t);

			if (have_published[i] && v == published[i])
				continue;
			published[i] = v;
			have_published[i] = 1;

			format(t, v, value, sizeof(value));
			snprintf(topic, sizeof(topic), "silt/%s/%s",
				 table->device, t->name);
			publish(client, topic, value);
		}

		if (now >= next_state) {
			next_state = now + STATE_MS;
			publish_state(client);
		}

		if (!MQTTClient_isConnected(client)) {
			fprintf(stderr, "silt-mqttd: broker gone, reconnecting\n");
			while (running &&
			       MQTTClient_connect(client, &opts) != MQTTCLIENT_SUCCESS)
				sleep(3);
			if (running) {
				MQTTClient_subscribe(client, sub, 0);
				memset(have_published, 0, sizeof(have_published));
			}
		}

		usleep(TICK_MS * 1000);
	}

	MQTTClient_disconnect(client, 1000);
	MQTTClient_destroy(&client);
	return EXIT_SUCCESS;
}
