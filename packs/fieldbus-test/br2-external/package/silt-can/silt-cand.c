/*
 * silt-cand - CAN for the simulated device.
 *
 * Sends the tag table onto a CAN bus and reads writes back off it. Tags
 * that name a "can" id are packed into frames: several tags may share one
 * id, each at its own byte offset, which is how a real device packs a
 * frame. The default pump puts temperature, pressure and rpm in 0x100 and
 * the fault bit in 0x101.
 *
 *   silt-cand [-i vcan0]
 *
 * Encoding: value * scale, rounded, big-endian across len bytes (1, 2 or
 * 4), which is the byte order most published CAN matrices use. Negative
 * values are two's complement in that width. The frame's length is the
 * highest byte any of its tags reaches.
 *
 * Receiving: a frame whose id matches is decoded into the writable tags
 * carried by that id, and nothing else. A simulated tag is never written
 * from the bus, so the sim core stays its only writer. That means a
 * frame the daemon sent itself changes nothing when it comes back on a
 * vcan loopback.
 */

#include "siltsim.h"

#include <errno.h>
#include <linux/can.h>
#include <linux/can/raw.h>
#include <math.h>
#include <net/if.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <unistd.h>

#define TICK_MS 10

static volatile sig_atomic_t running = 1;

static void on_stop(int sig) { (void)sig; running = 0; }

static void put_be(uint8_t *data, uint32_t at, uint32_t len, uint32_t raw)
{
	uint32_t i;

	for (i = 0; i < len; i++)
		data[at + i] = (uint8_t)(raw >> (8 * (len - 1 - i)));
}

static uint32_t get_be(const uint8_t *data, uint32_t at, uint32_t len)
{
	uint32_t v = 0, i;

	for (i = 0; i < len; i++)
		v = (v << 8) | data[at + i];
	return v;
}

static uint32_t encode(const struct siltsim_tag *t, double v)
{
	double scaled = round(v * t->can_scale);
	double limit = (t->can_len == 4) ? 4294967296.0 :
		       (t->can_len == 2) ? 65536.0 : 256.0;

	if (scaled < 0)
		scaled += limit;        /* two's complement in this width */
	if (scaled < 0)
		scaled = 0;
	if (scaled > limit - 1)
		scaled = limit - 1;
	return (uint32_t)scaled;
}

/*
 * Build the frame for one id out of the current table. Returns its length
 * in bytes, or 0 if no tag uses that id. Kept separate from the socket so
 * it can be tested without a CAN interface.
 */
uint8_t siltcan_build(const struct siltsim_table *tb, int32_t can_id, uint8_t *data)
{
	uint32_t i, dlc = 0;

	memset(data, 0, 8);
	for (i = 0; i < tb->tag_count; i++) {
		const struct siltsim_tag *t = &tb->tag[i];

		if (t->can_id != can_id)
			continue;
		put_be(data, t->can_byte, t->can_len, encode(t, siltsim_get(t)));
		if (t->can_byte + t->can_len > dlc)
			dlc = t->can_byte + t->can_len;
	}
	return (uint8_t)dlc;
}

/* A received frame into the writable tags that id carries. */
void siltcan_apply(struct siltsim_table *tb, int32_t can_id,
		   const uint8_t *data, uint8_t dlc)
{
	uint32_t i;

	for (i = 0; i < tb->tag_count; i++) {
		struct siltsim_tag *t = &tb->tag[i];

		if (t->can_id != can_id || !t->writable)
			continue;
		if (t->can_byte + t->can_len > dlc)
			continue;       /* the sender did not carry this tag */
		siltsim_set(t, (double)get_be(data, t->can_byte, t->can_len) /
				t->can_scale);
	}
}

/* Distinct ids in the table, with the shortest period any of their tags asks
 * for: one frame per id, sent as often as its most urgent tag wants. */
static uint32_t collect_ids(const struct siltsim_table *tb, int32_t *ids,
			    uint32_t *periods, uint32_t cap)
{
	uint32_t n = 0, i, k;

	for (i = 0; i < tb->tag_count; i++) {
		const struct siltsim_tag *t = &tb->tag[i];

		if (t->can_id < 0)
			continue;
		for (k = 0; k < n; k++)
			if (ids[k] == t->can_id)
				break;
		if (k == n) {
			if (n == cap)
				break;
			ids[n] = t->can_id;
			periods[n] = t->can_period_ms;
			n++;
		} else if (t->can_period_ms < periods[k]) {
			periods[k] = t->can_period_ms;
		}
	}
	return n;
}

static int open_can(const char *ifname)
{
	struct sockaddr_can addr;
	struct ifreq ifr;
	int s;

	s = socket(PF_CAN, SOCK_RAW, CAN_RAW);
	if (s < 0) {
		perror("silt-cand: socket");
		return -1;
	}

	memset(&ifr, 0, sizeof(ifr));
	strncpy(ifr.ifr_name, ifname, IFNAMSIZ - 1);
	if (ioctl(s, SIOCGIFINDEX, &ifr) < 0) {
		fprintf(stderr, "silt-cand: %s: %s (is the interface up?)\n",
			ifname, strerror(errno));
		close(s);
		return -1;
	}

	memset(&addr, 0, sizeof(addr));
	addr.can_family = AF_CAN;
	addr.can_ifindex = ifr.ifr_ifindex;
	if (bind(s, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
		perror("silt-cand: bind");
		close(s);
		return -1;
	}
	return s;
}

/* silt-cand-test.c compiles this file for siltcan_build and siltcan_apply,
 * and brings its own main. */
#ifndef SILTCAN_NO_MAIN

int main(int argc, char **argv)
{
	uint64_t next_send[SILTSIM_MAX_TAGS] = { 0 };
	uint32_t periods[SILTSIM_MAX_TAGS];
	int32_t ids[SILTSIM_MAX_TAGS];
	char err[SILTSIM_ERR_LEN] = "";
	const char *ifname = "vcan0";
	struct siltsim_table *tb;
	uint32_t seen_generation, id_count;
	int s, opt;

	while ((opt = getopt(argc, argv, "i:h")) != -1) {
		if (opt == 'i') {
			ifname = optarg;
		} else {
			fprintf(stderr, "usage: %s [-i interface]\n", argv[0]);
			return opt == 'h' ? EXIT_SUCCESS : EXIT_FAILURE;
		}
	}

	signal(SIGINT, on_stop);
	signal(SIGTERM, on_stop);

	for (;;) {
		tb = siltsim_attach(1, err, sizeof(err));
		if (tb && tb->tag_count > 0)
			break;
		fprintf(stderr, "silt-cand: waiting for the sim core: %s\n", err);
		sleep(2);
	}

	/* The interface is created by /etc/init.d/S45vcan, or by a CAN HAT's
	 * driver. Wait for it rather than exiting: a board that boots faster
	 * than its network should still end up serving. */
	while ((s = open_can(ifname)) < 0 && running)
		sleep(2);
	if (!running)
		return EXIT_SUCCESS;

	seen_generation = __atomic_load_n(&tb->generation, __ATOMIC_ACQUIRE);
	id_count = collect_ids(tb, ids, periods, SILTSIM_MAX_TAGS);
	fprintf(stderr, "silt-cand: \"%s\" generation %u, %u frame id(s) on %s\n",
		tb->device, tb->generation, id_count, ifname);

	while (running) {
		struct timeval tv = { 0, TICK_MS * 1000 };
		uint64_t now = siltsim_now_ms();
		struct can_frame frame;
		fd_set r;
		uint32_t k, gen;

		gen = __atomic_load_n(&tb->generation, __ATOMIC_ACQUIRE);
		if (gen != seen_generation) {
			seen_generation = gen;
			id_count = collect_ids(tb, ids, periods, SILTSIM_MAX_TAGS);
			fprintf(stderr, "silt-cand: reloaded, %u frame id(s)\n", id_count);
		}

		for (k = 0; k < id_count; k++) {
			if (now < next_send[k])
				continue;
			next_send[k] = now + periods[k];

			memset(&frame, 0, sizeof(frame));
			frame.can_id = (canid_t)ids[k];
			if (ids[k] > 0x7FF)
				frame.can_id |= CAN_EFF_FLAG;
			frame.can_dlc = siltcan_build(tb, ids[k], frame.data);
			if (write(s, &frame, sizeof(frame)) != sizeof(frame))
				perror("silt-cand: write");
		}

		FD_ZERO(&r);
		FD_SET(s, &r);
		if (select(s + 1, &r, NULL, NULL, &tv) > 0 && FD_ISSET(s, &r)) {
			ssize_t n = read(s, &frame, sizeof(frame));

			if (n == (ssize_t)sizeof(frame))
				siltcan_apply(tb, (int32_t)(frame.can_id & CAN_EFF_MASK),
					      frame.data, frame.can_dlc);
		}
	}

	close(s);
	return EXIT_SUCCESS;
}

#endif /* SILTCAN_NO_MAIN */
