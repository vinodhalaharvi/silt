/*
 * The smallest OPC UA server worth connecting to: open62541's default
 * server on port 4840, anonymous, no encryption, with two variables under
 * Objects so a client has something to read.
 *
 *   ns=1;s=silt.hello    String  "hello from silt", fixed
 *   ns=1;s=silt.counter  UInt32  incremented once a second
 *
 * The counter is the useful one: a client subscribed to it sees the value
 * change, which proves browsing, reads and subscriptions end to end.
 * A bench fixture, not a template for a production server.
 */

#include <open62541/plugin/log_stdout.h>
#include <open62541/server.h>
#include <open62541/server_config_default.h>

#include <signal.h>
#include <stdlib.h>

static volatile UA_Boolean running = true;
static UA_UInt32 counter;

static void stop(int sig)
{
	(void)sig;
	running = false;
}

static void tick(UA_Server *server, void *data)
{
	UA_Variant v;

	(void)data;
	counter++;
	UA_Variant_setScalar(&v, &counter, &UA_TYPES[UA_TYPES_UINT32]);
	UA_Server_writeValue(server, UA_NODEID_STRING(1, "silt.counter"), v);
}

static UA_StatusCode add_variable(UA_Server *server, char *id, void *value,
				  const UA_DataType *type)
{
	UA_VariableAttributes attr = UA_VariableAttributes_default;

	UA_Variant_setScalar(&attr.value, value, type);
	attr.displayName = UA_LOCALIZEDTEXT("en-US", id);
	attr.accessLevel = UA_ACCESSLEVELMASK_READ;

	return UA_Server_addVariableNode(server,
		UA_NODEID_STRING(1, id),
		UA_NODEID_NUMERIC(0, UA_NS0ID_OBJECTSFOLDER),
		UA_NODEID_NUMERIC(0, UA_NS0ID_ORGANIZES),
		UA_QUALIFIEDNAME(1, id),
		UA_NODEID_NUMERIC(0, UA_NS0ID_BASEDATAVARIABLETYPE),
		attr, NULL, NULL);
}

int main(void)
{
	UA_String hello = UA_STRING("hello from silt");
	UA_Server *server;
	UA_StatusCode rc;

	signal(SIGINT, stop);
	signal(SIGTERM, stop);

	server = UA_Server_new();
	UA_ServerConfig_setDefault(UA_Server_getConfig(server));

	if (add_variable(server, "silt.hello", &hello,
			 &UA_TYPES[UA_TYPES_STRING]) != UA_STATUSCODE_GOOD ||
	    add_variable(server, "silt.counter", &counter,
			 &UA_TYPES[UA_TYPES_UINT32]) != UA_STATUSCODE_GOOD) {
		UA_Server_delete(server);
		return EXIT_FAILURE;
	}

	UA_Server_addRepeatedCallback(server, tick, NULL, 1000, NULL);

	rc = UA_Server_run(server, &running);
	UA_Server_delete(server);
	return rc == UA_STATUSCODE_GOOD ? EXIT_SUCCESS : EXIT_FAILURE;
}
