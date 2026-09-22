/*
 * silt-httpd - push a device config over HTTP, read the live values back.
 *
 *   POST /config     a device config; validated before anything changes
 *   GET  /config     the config now running
 *   GET  /tags       every tag with its live value, as JSON
 *   GET  /health     generation, tag count, uptime
 *
 * Example:
 *
 *   curl -X POST --data-binary @pump.json http://board:8080/config
 *   curl http://board:8080/tags
 *
 * A POST is validated with the same code the sim core uses, so a config
 * this accepts is one the sim core will run. If it does not validate, the
 * reply says why and the running device is untouched: the file is not
 * written and no reload is asked for.
 *
 * No authentication and no TLS, deliberately. The board already serves
 * anonymous OPC UA, unauthenticated Modbus writes and an open CAN bus; a
 * password on this endpoint alone would suggest a protection the rest of
 * the device does not have. Put it on a lab network, not a plant one.
 */

#include "siltsim.h"

#include <microhttpd.h>

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#define PORT          8080
#define MAX_BODY      (1024 * 1024)
#define PIDFILE       "/var/run/silt-sim.pid"

/* libmicrohttpd 0.9.71 renamed the result enum; Buildroot ships 1.0.5 and
 * Debian 1.0.0, but the older name still turns up on other distributions. */
#if MHD_VERSION >= 0x00097002
typedef enum MHD_Result mhd_result;
#else
typedef int mhd_result;
#endif

static const char *config_path = "/etc/silt-sim.json";
static volatile sig_atomic_t running = 1;
static struct siltsim_table *table;

static void on_stop(int sig) { (void)sig; running = 0; }

struct post {
	char  *body;
	size_t len;
};

static mhd_result respond(struct MHD_Connection *c, unsigned code,
			  const char *type, const char *body)
{
	struct MHD_Response *r;
	mhd_result rc;

	r = MHD_create_response_from_buffer(strlen(body), (void *)body,
					    MHD_RESPMEM_MUST_COPY);
	MHD_add_response_header(r, "Content-Type", type);
	rc = MHD_queue_response(c, code, r);
	MHD_destroy_response(r);
	return rc;
}

/* Write the file so a crash or a power cut leaves either the old config or
 * the new one, never half of either. */
static int save_config(const char *body, size_t len, char *err, size_t errlen)
{
	char tmp[512];
	FILE *f;
	int fd;

	snprintf(tmp, sizeof(tmp), "%s.new", config_path);
	f = fopen(tmp, "wb");
	if (!f) {
		snprintf(err, errlen, "cannot write %s", tmp);
		return -1;
	}
	if (fwrite(body, 1, len, f) != len) {
		snprintf(err, errlen, "short write to %s", tmp);
		fclose(f);
		return -1;
	}
	fflush(f);
	fd = fileno(f);
	if (fd >= 0)
		fsync(fd);
	fclose(f);

	if (rename(tmp, config_path) != 0) {
		snprintf(err, errlen, "cannot replace %s", config_path);
		return -1;
	}
	return 0;
}

/* Ask the sim core to reload. It validates again and keeps the old table if
 * anything is wrong, so this cannot leave the device dead. */
static int reload_sim(char *err, size_t errlen)
{
	int pid = 0;
	FILE *f;

	f = fopen(PIDFILE, "r");
	if (!f || fscanf(f, "%d", &pid) != 1 || pid <= 0) {
		if (f)
			fclose(f);
		snprintf(err, errlen, "cannot read %s: is silt-sim running?", PIDFILE);
		return -1;
	}
	fclose(f);

	if (kill(pid, SIGHUP) != 0) {
		snprintf(err, errlen, "cannot signal silt-sim (pid %d)", pid);
		return -1;
	}
	return 0;
}

static void summary(const struct siltsim_table *t, char *out, size_t cap)
{
	uint32_t i, mb = 0, ua = 0, can = 0;

	for (i = 0; i < t->tag_count; i++) {
		if (t->tag[i].mb_addr >= 0)
			mb++;
		if (t->tag[i].opcua_node[0])
			ua++;
		if (t->tag[i].can_id >= 0)
			can++;
	}
	snprintf(out, cap,
		 "{\"ok\":true,\"device\":\"%s\",\"tags\":%u,"
		 "\"modbus\":%u,\"opcua\":%u,\"can\":%u}\n",
		 t->device, t->tag_count, mb, ua, can);
}

static mhd_result post_config(struct MHD_Connection *c, struct post *p)
{
	static struct siltsim_table fresh;   /* large: not on the stack */
	char err[SILTSIM_ERR_LEN] = "";
	char msg[SILTSIM_ERR_LEN + 64];

	if (!p->body || p->len == 0)
		return respond(c, MHD_HTTP_BAD_REQUEST, "text/plain",
			       "empty body: POST the config as the request body\n");

	if (siltsim_load_json(p->body, p->len, &fresh, err, sizeof(err)) != 0) {
		snprintf(msg, sizeof(msg), "%s\n", err);
		fprintf(stderr, "silt-httpd: rejected a config: %s\n", err);
		return respond(c, MHD_HTTP_BAD_REQUEST, "text/plain", msg);
	}

	if (save_config(p->body, p->len, err, sizeof(err)) != 0 ||
	    reload_sim(err, sizeof(err)) != 0) {
		snprintf(msg, sizeof(msg), "%s\n", err);
		return respond(c, MHD_HTTP_INTERNAL_SERVER_ERROR, "text/plain", msg);
	}

	fprintf(stderr, "silt-httpd: accepted \"%s\", %u tag(s)\n",
		fresh.device, fresh.tag_count);
	summary(&fresh, msg, sizeof(msg));
	return respond(c, MHD_HTTP_OK, "application/json", msg);
}

static mhd_result get_config(struct MHD_Connection *c)
{
	char *body;
	long size;
	FILE *f;

	f = fopen(config_path, "rb");
	if (!f)
		return respond(c, MHD_HTTP_NOT_FOUND, "text/plain",
			       "no config file\n");
	fseek(f, 0, SEEK_END);
	size = ftell(f);
	rewind(f);
	if (size < 0 || size > MAX_BODY) {
		fclose(f);
		return respond(c, MHD_HTTP_INTERNAL_SERVER_ERROR, "text/plain",
			       "config file is not readable\n");
	}
	body = malloc((size_t)size + 1);
	if (!body) {
		fclose(f);
		return respond(c, MHD_HTTP_INTERNAL_SERVER_ERROR, "text/plain",
			       "out of memory\n");
	}
	if (fread(body, 1, (size_t)size, f) != (size_t)size) {
		free(body);
		fclose(f);
		return respond(c, MHD_HTTP_INTERNAL_SERVER_ERROR, "text/plain",
			       "short read on the config file\n");
	}
	fclose(f);
	body[size] = '\0';

	{
		mhd_result rc = respond(c, MHD_HTTP_OK, "application/json", body);

		free(body);
		return rc;
	}
}

static const char *type_name(uint32_t type)
{
	return type == SILTSIM_BOOL ? "bool" : type == SILTSIM_INT ? "int" : "float";
}

/* Live values, so a buyer can see what the device is doing without a
 * protocol client at all. */
static mhd_result get_tags(struct MHD_Connection *c)
{
	size_t cap = 256 + (size_t)table->tag_count * 320;
	char *body = malloc(cap);
	size_t at = 0;
	uint32_t i;

	if (!body)
		return respond(c, MHD_HTTP_INTERNAL_SERVER_ERROR, "text/plain",
			       "out of memory\n");

	at += (size_t)snprintf(body + at, cap - at,
			       "{\"device\":\"%s\",\"generation\":%u,\"tags\":[",
			       table->device,
			       __atomic_load_n(&table->generation, __ATOMIC_ACQUIRE));

	for (i = 0; i < table->tag_count && at < cap; i++) {
		const struct siltsim_tag *t = &table->tag[i];

		at += (size_t)snprintf(body + at, cap - at,
			"%s{\"name\":\"%s\",\"value\":%.4f,\"type\":\"%s\","
			"\"unit\":\"%s\",\"writable\":%s,"
			"\"modbus\":%d,\"opcua\":\"%s\",\"can\":%d}",
			i ? "," : "", t->name, siltsim_get(t), type_name(t->type),
			t->unit, t->writable ? "true" : "false",
			t->mb_addr, t->opcua_node, t->can_id);
	}
	if (at < cap)
		at += (size_t)snprintf(body + at, cap - at, "]}\n");

	{
		mhd_result rc = respond(c, MHD_HTTP_OK, "application/json", body);

		free(body);
		return rc;
	}
}

static mhd_result get_health(struct MHD_Connection *c)
{
	char body[256];

	snprintf(body, sizeof(body),
		 "{\"ok\":true,\"device\":\"%s\",\"generation\":%u,\"tags\":%u,"
		 "\"uptime_s\":%llu}\n",
		 table->device,
		 __atomic_load_n(&table->generation, __ATOMIC_ACQUIRE),
		 table->tag_count,
		 (unsigned long long)((siltsim_now_ms() - table->started_ms) / 1000));
	return respond(c, MHD_HTTP_OK, "application/json", body);
}

static mhd_result on_request(void *cls, struct MHD_Connection *c,
			     const char *url, const char *method,
			     const char *version, const char *data,
			     size_t *data_size, void **con_cls)
{
	struct post *p = *con_cls;

	(void)cls; (void)version;

	if (!strcmp(method, "GET")) {
		if (!strcmp(url, "/config"))
			return get_config(c);
		if (!strcmp(url, "/tags"))
			return get_tags(c);
		if (!strcmp(url, "/health") || !strcmp(url, "/"))
			return get_health(c);
		return respond(c, MHD_HTTP_NOT_FOUND, "text/plain",
			       "try /config, /tags or /health\n");
	}

	if (strcmp(method, "POST") != 0)
		return respond(c, MHD_HTTP_METHOD_NOT_ALLOWED, "text/plain",
			       "GET or POST only\n");

	if (strcmp(url, "/config") != 0)
		return respond(c, MHD_HTTP_NOT_FOUND, "text/plain",
			       "POST goes to /config\n");

	/* First call for this request: set up the body buffer. */
	if (!p) {
		p = calloc(1, sizeof(*p));
		if (!p)
			return MHD_NO;
		*con_cls = p;
		return MHD_YES;
	}

	/* Body arrives in pieces; the last call has none left. */
	if (*data_size) {
		char *grown;

		if (p->len + *data_size > MAX_BODY) {
			free(p->body);
			p->body = NULL;
			p->len = 0;
			return respond(c, MHD_HTTP_CONTENT_TOO_LARGE, "text/plain",
				       "config larger than 1MB\n");
		}
		grown = realloc(p->body, p->len + *data_size + 1);
		if (!grown)
			return MHD_NO;
		p->body = grown;
		memcpy(p->body + p->len, data, *data_size);
		p->len += *data_size;
		p->body[p->len] = '\0';
		*data_size = 0;
		return MHD_YES;
	}

	return post_config(c, p);
}

static void on_done(void *cls, struct MHD_Connection *c, void **con_cls,
		    enum MHD_RequestTerminationCode code)
{
	struct post *p = *con_cls;

	(void)cls; (void)c; (void)code;
	if (p) {
		free(p->body);
		free(p);
		*con_cls = NULL;
	}
}

int main(int argc, char **argv)
{
	char err[SILTSIM_ERR_LEN] = "";
	struct MHD_Daemon *d;
	uint16_t port = PORT;
	int opt;

	while ((opt = getopt(argc, argv, "c:p:h")) != -1) {
		switch (opt) {
		case 'c': config_path = optarg; break;
		case 'p': port = (uint16_t)atoi(optarg); break;
		default:
			fprintf(stderr, "usage: %s [-c config] [-p port]\n", argv[0]);
			return opt == 'h' ? EXIT_SUCCESS : EXIT_FAILURE;
		}
	}

	signal(SIGINT, on_stop);
	signal(SIGTERM, on_stop);
	signal(SIGPIPE, SIG_IGN);

	for (;;) {
		table = siltsim_attach(0, err, sizeof(err));
		if (table && table->tag_count > 0)
			break;
		fprintf(stderr, "silt-httpd: waiting for the sim core: %s\n", err);
		sleep(2);
	}

	d = MHD_start_daemon(MHD_USE_INTERNAL_POLLING_THREAD, port, NULL, NULL,
			     &on_request, NULL,
			     MHD_OPTION_NOTIFY_COMPLETED, &on_done, NULL,
			     MHD_OPTION_CONNECTION_LIMIT, 16,
			     MHD_OPTION_END);
	if (!d) {
		fprintf(stderr, "silt-httpd: cannot listen on %u\n", port);
		return EXIT_FAILURE;
	}
	fprintf(stderr, "silt-httpd: listening on 0.0.0.0:%u, config %s\n",
		port, config_path);

	while (running)
		sleep(1);

	MHD_stop_daemon(d);
	return EXIT_SUCCESS;
}
