/*
 * silt-opcuad - OPC UA for the simulated device.
 *
 * Serves port 4840 from the tag table in /dev/shm. Every tag with an
 * "opcua" name in the config becomes a variable under Objects, as
 * ns=1;s=<device>.<node>, so the default pump appears as
 * ns=1;s=pump-1.Temperature and so on.
 *
 * Reads:  the loop copies the table into the nodes ten times a second.
 * Writes: a writable tag's node gets a write callback that copies the new
 *         value into the table, where Modbus and CAN see it. Simulated
 *         tags are read-only, so the sim core stays their only writer.
 *
 * open62541 chooses its listening addresses with getaddrinfo(AI_ADDRCONFIG),
 * which returns IPv4 only once the machine has a non-loopback IPv4 address.
 * Started before DHCP, it would listen on IPv6 alone and every IPv4 client
 * would be refused, so it waits for an address before serving. The init
 * script waits too; this is the same check in the program, for anyone who
 * starts it by hand.
 */

#include "siltsim.h"

#include <open62541/plugin/log_stdout.h>
#include <open62541/server.h>
#include <open62541/server_config_default.h>

#include <ifaddrs.h>
#include <net/if.h>
#include <netinet/in.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

#define PORT      4840
#define TICK_MS   100
#define WAIT_S    60

static volatile UA_Boolean running = true;
static struct siltsim_table *table;

static void on_stop(int sig) { (void)sig; running = false; }

/* Is there a non-loopback IPv4 address yet? */
static int have_ipv4(void)
{
	struct ifaddrs *list, *a;
	int found = 0;

	if (getifaddrs(&list) != 0)
		return 0;
	for (a = list; a && !found; a = a->ifa_next)
		if (a->ifa_addr && a->ifa_addr->sa_family == AF_INET &&
		    !(a->ifa_flags & IFF_LOOPBACK) && (a->ifa_flags & IFF_UP))
			found = 1;
	freeifaddrs(list);
	return found;
}

static void to_variant(const struct siltsim_tag *t, double v, UA_Variant *out)
{
	static UA_Boolean b;
	static UA_Int32 i;
	static UA_Double d;

	switch (t->type) {
	case SILTSIM_BOOL:
		b = v != 0;
		UA_Variant_setScalarCopy(out, &b, &UA_TYPES[UA_TYPES_BOOLEAN]);
		break;
	case SILTSIM_INT:
		i = (UA_Int32)v;
		UA_Variant_setScalarCopy(out, &i, &UA_TYPES[UA_TYPES_INT32]);
		break;
	default:
		d = v;
		UA_Variant_setScalarCopy(out, &d, &UA_TYPES[UA_TYPES_DOUBLE]);
		break;
	}
}

static int from_variant(const UA_Variant *v, double *out)
{
	if (UA_Variant_hasScalarType(v, &UA_TYPES[UA_TYPES_BOOLEAN]))
		*out = *(UA_Boolean *)v->data ? 1 : 0;
	else if (UA_Variant_hasScalarType(v, &UA_TYPES[UA_TYPES_INT32]))
		*out = *(UA_Int32 *)v->data;
	else if (UA_Variant_hasScalarType(v, &UA_TYPES[UA_TYPES_DOUBLE]))
		*out = *(UA_Double *)v->data;
	else if (UA_Variant_hasScalarType(v, &UA_TYPES[UA_TYPES_FLOAT]))
		*out = *(UA_Float *)v->data;
	else
		return -1;
	return 0;
}

/* A client wrote a writable tag: put it in the table for everyone else. */
static void on_write(UA_Server *server, const UA_NodeId *sessionId,
		     void *sessionContext, const UA_NodeId *nodeId,
		     void *nodeContext, const UA_NumericRange *range,
		     const UA_DataValue *data)
{
	struct siltsim_tag *t = nodeContext;
	double v;

	(void)server; (void)sessionId; (void)sessionContext;
	(void)nodeId; (void)range;

	if (!t || !data->hasValue)
		return;
	if (from_variant(&data->value, &v) == 0)
		siltsim_set(t, v);
}

#define ID_LEN (SILTSIM_NAME_LEN + SILTSIM_NODE_LEN + 2)

/*
 * The ids this server has actually added. A reload replaces the table
 * before we notice, so the old device's node names are no longer anywhere
 * in shared memory: deleting them from the new table's contents removes
 * nothing and leaves the old device visible beside the new one. Keep our
 * own list instead.
 */
static char added[SILTSIM_MAX_TAGS][ID_LEN];
static uint32_t added_count;

static void node_id_of(const struct siltsim_table *tb,
		       const struct siltsim_tag *t, char *buf, size_t cap)
{
	snprintf(buf, cap, "%s.%s", tb->device, t->opcua_node);
}

static int add_nodes(UA_Server *server, struct siltsim_table *tb)
{
	uint32_t i, n = 0;

	added_count = 0;
	for (i = 0; i < tb->tag_count; i++) {
		struct siltsim_tag *t = &tb->tag[i];
		UA_VariableAttributes attr = UA_VariableAttributes_default;
		char id[ID_LEN];
		UA_StatusCode rc;

		if (t->opcua_node[0] == '\0')
			continue;
		node_id_of(tb, t, id, sizeof(id));

		to_variant(t, siltsim_get(t), &attr.value);
		attr.displayName = UA_LOCALIZEDTEXT("en-US", t->opcua_node);
		if (t->unit[0])
			attr.description = UA_LOCALIZEDTEXT("en-US", t->unit);
		attr.accessLevel = UA_ACCESSLEVELMASK_READ;
		if (t->writable)
			attr.accessLevel |= UA_ACCESSLEVELMASK_WRITE;

		rc = UA_Server_addVariableNode(server,
			UA_NODEID_STRING(1, id),
			UA_NODEID_NUMERIC(0, UA_NS0ID_OBJECTSFOLDER),
			UA_NODEID_NUMERIC(0, UA_NS0ID_ORGANIZES),
			UA_QUALIFIEDNAME(1, t->opcua_node),
			UA_NODEID_NUMERIC(0, UA_NS0ID_BASEDATAVARIABLETYPE),
			attr, t, NULL);
		UA_Variant_clear(&attr.value);
		if (rc != UA_STATUSCODE_GOOD) {
			fprintf(stderr, "silt-opcuad: cannot add %s: %s\n",
				id, UA_StatusCode_name(rc));
			return -1;
		}

		if (t->writable) {
			UA_ValueCallback cb = { NULL, on_write };

			UA_Server_setVariableNode_valueCallback(server,
				UA_NODEID_STRING(1, id), cb);
		}
		snprintf(added[added_count++], ID_LEN, "%s", id);
		n++;
	}

	fprintf(stderr, "silt-opcuad: \"%s\" generation %u, %u node(s) on OPC UA\n",
		tb->device, tb->generation, n);
	return 0;
}

static void remove_nodes(UA_Server *server)
{
	uint32_t i;

	for (i = 0; i < added_count; i++)
		UA_Server_deleteNode(server, UA_NODEID_STRING(1, added[i]), true);
	added_count = 0;
}

/*
 * The table into the nodes, writable tags included: a setpoint written
 * over Modbus has to show up here too, which is the whole point of one
 * shared table. Writing a node fires on_write, which puts the same value
 * back in the table, so only changed values are published: an unchanged
 * one would be a write, a callback and a store every tick for nothing.
 */
static void publish_values(UA_Server *server, struct siltsim_table *tb,
			   double *published)
{
	uint32_t i;

	for (i = 0; i < tb->tag_count; i++) {
		struct siltsim_tag *t = &tb->tag[i];
		char id[ID_LEN];
		double v = siltsim_get(t);
		UA_Variant var;

		if (t->opcua_node[0] == '\0')
			continue;
		if (v == published[i])
			continue;
		published[i] = v;

		node_id_of(tb, t, id, sizeof(id));
		UA_Variant_init(&var);
		to_variant(t, v, &var);
		UA_Server_writeValue(server, UA_NODEID_STRING(1, id), var);
		UA_Variant_clear(&var);
	}
}

int main(void)
{
	static double published[SILTSIM_MAX_TAGS];
	char err[SILTSIM_ERR_LEN] = "";
	uint32_t seen_generation;
	UA_Server *server;
	int waited = 0;
	uint32_t i;

	signal(SIGINT, on_stop);
	signal(SIGTERM, on_stop);

	for (;;) {
		table = siltsim_attach(1, err, sizeof(err));
		if (table && table->tag_count > 0)
			break;
		fprintf(stderr, "silt-opcuad: waiting for the sim core: %s\n", err);
		sleep(2);
	}

	while (!have_ipv4() && waited < WAIT_S) {
		if (waited == 0)
			fprintf(stderr, "silt-opcuad: waiting for an IPv4 address\n");
		sleep(1);
		waited++;
	}

	server = UA_Server_new();
	UA_ServerConfig_setMinimal(UA_Server_getConfig(server), PORT, NULL);

	seen_generation = __atomic_load_n(&table->generation, __ATOMIC_ACQUIRE);
	if (add_nodes(server, table) != 0)
		return EXIT_FAILURE;
	for (i = 0; i < SILTSIM_MAX_TAGS; i++)
		published[i] = siltsim_get(&table->tag[i]);

	if (UA_Server_run_startup(server) != UA_STATUSCODE_GOOD) {
		fprintf(stderr, "silt-opcuad: cannot listen on %d\n", PORT);
		return EXIT_FAILURE;
	}
	fprintf(stderr, "silt-opcuad: listening on :%d\n", PORT);

	while (running) {
		uint32_t gen = __atomic_load_n(&table->generation, __ATOMIC_ACQUIRE);

		if (gen != seen_generation) {
			/* The config changed: the node set changes with it. */
			remove_nodes(server);
			seen_generation = gen;
			if (add_nodes(server, table) != 0)
				break;
			for (i = 0; i < SILTSIM_MAX_TAGS; i++)
				published[i] = siltsim_get(&table->tag[i]);
		}

		publish_values(server, table, published);
		UA_Server_run_iterate(server, false);
		usleep(TICK_MS * 1000);
	}

	UA_Server_run_shutdown(server);
	UA_Server_delete(server);
	return EXIT_SUCCESS;
}
