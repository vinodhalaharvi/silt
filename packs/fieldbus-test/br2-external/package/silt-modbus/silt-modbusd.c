/*
 * silt-modbusd - Modbus TCP for the simulated device.
 *
 * Serves port 502 from the tag table in /dev/shm. Which tag sits at which
 * address comes from the table, so this program never reads the config
 * file: the sim core is the only parser, and the mapping cannot drift
 * between the two.
 *
 * Reads:  values are copied from the table into libmodbus's mapping just
 *         before each reply, so a client always gets the current value.
 * Writes: after a reply, registers and coils belonging to writable tags
 *         are copied back into the table, where every other protocol sees
 *         them. A tag that is simulated is never written back, so the sim
 *         core stays its only writer.
 *
 * It binds with modbus_new_tcp(NULL, ...), which is INADDR_ANY, so unlike
 * the OPC UA server it can start before DHCP has finished.
 */

#include "siltsim.h"

#include <modbus/modbus.h>

#include <errno.h>
#include <math.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <unistd.h>

#define PORT         502
#define MAX_CLIENTS  8
#define NB_BITS      512
#define NB_REGISTERS 512

static volatile sig_atomic_t running = 1;

static void on_stop(int sig) { (void)sig; running = 0; }

static uint16_t to_register(const struct siltsim_tag *t, double v)
{
	double scaled = round(v * t->mb_scale);

	if (scaled < 0)
		scaled += 65536;        /* two's complement, as Modbus clients read it */
	if (scaled < 0)
		scaled = 0;
	if (scaled > 65535)
		scaled = 65535;
	return (uint16_t)scaled;
}

static double from_register(const struct siltsim_tag *t, uint16_t raw)
{
	return (double)raw / t->mb_scale;
}

/* The table's values into libmodbus's view of the world. */
static void table_to_mapping(const struct siltsim_table *tb, modbus_mapping_t *map)
{
	uint32_t i;

	for (i = 0; i < tb->tag_count; i++) {
		const struct siltsim_tag *t = &tb->tag[i];
		double v = siltsim_get(t);

		if (t->mb_addr < 0)
			continue;

		switch (t->mb_table) {
		case SILTSIM_MB_HOLDING:
			if (t->mb_addr < map->nb_registers)
				map->tab_registers[t->mb_addr] = to_register(t, v);
			break;
		case SILTSIM_MB_INPUT:
			if (t->mb_addr < map->nb_input_registers)
				map->tab_input_registers[t->mb_addr] = to_register(t, v);
			break;
		case SILTSIM_MB_COIL:
			if (t->mb_addr < map->nb_bits)
				map->tab_bits[t->mb_addr] = v != 0;
			break;
		case SILTSIM_MB_DISCRETE:
			if (t->mb_addr < map->nb_input_bits)
				map->tab_input_bits[t->mb_addr] = v != 0;
			break;
		default:
			break;
		}
	}
}

/* What a client wrote, back into the table. Writable tags only. */
static void mapping_to_table(struct siltsim_table *tb, const modbus_mapping_t *map)
{
	uint32_t i;

	for (i = 0; i < tb->tag_count; i++) {
		struct siltsim_tag *t = &tb->tag[i];

		if (!t->writable || t->mb_addr < 0)
			continue;

		if (t->mb_table == SILTSIM_MB_HOLDING && t->mb_addr < map->nb_registers) {
			double v = from_register(t, map->tab_registers[t->mb_addr]);

			if (v != siltsim_get(t))
				siltsim_set(t, v);
		} else if (t->mb_table == SILTSIM_MB_COIL && t->mb_addr < map->nb_bits) {
			double v = map->tab_bits[t->mb_addr] ? 1 : 0;

			if (v != siltsim_get(t))
				siltsim_set(t, v);
		}
	}
}

static void report(const struct siltsim_table *tb)
{
	uint32_t i, n = 0;

	for (i = 0; i < tb->tag_count; i++)
		if (tb->tag[i].mb_addr >= 0)
			n++;
	fprintf(stderr, "silt-modbusd: \"%s\" generation %u, %u tag(s) on Modbus\n",
		tb->device, tb->generation, n);
}

int main(void)
{
	uint8_t query[MODBUS_TCP_MAX_ADU_LENGTH];
	char err[SILTSIM_ERR_LEN] = "";
	struct siltsim_table *tb;
	modbus_mapping_t *map;
	modbus_t *ctx;
	uint32_t seen_generation;
	int listener, fdmax, fd;
	fd_set all;

	signal(SIGINT, on_stop);
	signal(SIGTERM, on_stop);
	signal(SIGPIPE, SIG_IGN);

	/* The sim core creates the table; wait rather than race it at boot. */
	for (;;) {
		tb = siltsim_attach(1, err, sizeof(err));
		if (tb && tb->tag_count > 0)
			break;
		fprintf(stderr, "silt-modbusd: waiting for the sim core: %s\n", err);
		sleep(2);
	}
	seen_generation = __atomic_load_n(&tb->generation, __ATOMIC_ACQUIRE);
	report(tb);

	ctx = modbus_new_tcp(NULL, PORT);
	map = modbus_mapping_new(NB_BITS, NB_BITS, NB_REGISTERS, NB_REGISTERS);
	if (!ctx || !map) {
		fprintf(stderr, "silt-modbusd: %s\n", modbus_strerror(errno));
		return EXIT_FAILURE;
	}

	listener = modbus_tcp_listen(ctx, MAX_CLIENTS);
	if (listener == -1) {
		fprintf(stderr, "silt-modbusd: listen on %d: %s\n",
			PORT, modbus_strerror(errno));
		return EXIT_FAILURE;
	}
	fprintf(stderr, "silt-modbusd: listening on 0.0.0.0:%d\n", PORT);

	FD_ZERO(&all);
	FD_SET(listener, &all);
	fdmax = listener;

	while (running) {
		struct timeval tv = { 1, 0 };
		fd_set ready = all;
		uint32_t gen;

		if (select(fdmax + 1, &ready, NULL, NULL, &tv) == -1) {
			if (errno == EINTR)
				continue;
			perror("silt-modbusd: select");
			break;
		}

		gen = __atomic_load_n(&tb->generation, __ATOMIC_ACQUIRE);
		if (gen != seen_generation) {
			seen_generation = gen;
			report(tb);   /* the config changed under us */
		}

		for (fd = 0; fd <= fdmax; fd++) {
			int rc;

			if (!FD_ISSET(fd, &ready))
				continue;

			if (fd == listener) {
				int client = modbus_tcp_accept(ctx, &listener);

				if (client == -1)
					continue;
				FD_SET(client, &all);
				if (client > fdmax)
					fdmax = client;
				continue;
			}

			modbus_set_socket(ctx, fd);
			rc = modbus_receive(ctx, query);
			if (rc > 0) {
				table_to_mapping(tb, map);
				modbus_reply(ctx, query, rc, map);
				mapping_to_table(tb, map);
			} else if (rc == -1) {
				close(fd);
				FD_CLR(fd, &all);
			}
		}
	}

	close(listener);
	modbus_mapping_free(map);
	modbus_free(ctx);
	return EXIT_SUCCESS;
}
