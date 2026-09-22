/*
 * The smallest Modbus TCP server worth connecting to: libmodbus on port
 * 502, any unit id, several clients at once.
 *
 *   holding register 0   counter, incremented once a second
 *   holding register 1   0x5117, fixed, to prove you are reading this server
 *   holding registers 2-9  scratch, writable: write, read back
 *   coils 0-7            scratch, writable
 *
 * The counter is the useful one: two reads a few seconds apart differ,
 * which proves the server is live and not a cached answer.
 *
 * It binds with modbus_new_tcp(NULL, ...), which libmodbus turns into
 * INADDR_ANY. Unlike open62541's getaddrinfo(AI_ADDRCONFIG), that does not
 * depend on the machine having an address yet, so it can start before DHCP.
 * A bench fixture, not a template for a production server.
 */

#include <modbus/modbus.h>

#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/select.h>
#include <time.h>
#include <unistd.h>

#define PORT 502
#define MAX_CLIENTS 8

static volatile sig_atomic_t running = 1;

static void stop(int sig)
{
	(void)sig;
	running = 0;
}

int main(void)
{
	uint8_t query[MODBUS_TCP_MAX_ADU_LENGTH];
	modbus_mapping_t *map;
	modbus_t *ctx;
	fd_set all;
	int listener, fdmax;
	time_t last;

	signal(SIGINT, stop);
	signal(SIGTERM, stop);
	signal(SIGPIPE, SIG_IGN);

	ctx = modbus_new_tcp(NULL, PORT);
	map = modbus_mapping_new(8, 0, 10, 0);
	if (!ctx || !map) {
		fprintf(stderr, "hello-modbus: %s\n", modbus_strerror(errno));
		return EXIT_FAILURE;
	}
	map->tab_registers[1] = 0x5117;

	listener = modbus_tcp_listen(ctx, MAX_CLIENTS);
	if (listener == -1) {
		fprintf(stderr, "hello-modbus: listen on %d: %s\n", PORT,
			modbus_strerror(errno));
		return EXIT_FAILURE;
	}
	fprintf(stderr, "hello-modbus: listening on 0.0.0.0:%d\n", PORT);

	FD_ZERO(&all);
	FD_SET(listener, &all);
	fdmax = listener;
	last = time(NULL);

	while (running) {
		struct timeval tv = { 1, 0 };
		fd_set ready = all;
		int fd;

		if (select(fdmax + 1, &ready, NULL, NULL, &tv) == -1) {
			if (errno == EINTR)
				continue;
			perror("hello-modbus: select");
			break;
		}

		if (time(NULL) != last) {
			last = time(NULL);
			map->tab_registers[0]++;
		}

		for (fd = 0; fd <= fdmax; fd++) {
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
			int rc = modbus_receive(ctx, query);
			if (rc > 0) {
				modbus_reply(ctx, query, rc, map);
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
