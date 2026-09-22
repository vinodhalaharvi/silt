/*
 * silt-simd - the simulation core.
 *
 * Reads the device config, publishes the tag table into /dev/shm, and
 * moves the simulated values. It is the only process that parses JSON, and
 * the only writer of any tag that has a simulated behaviour.
 *
 *   silt-simd [-c config] [-f]     -f stays in the foreground
 *   silt-simd -t [-c config]       check the config and exit: 0 if it is
 *                                  usable, 1 and one line saying why if
 *                                  not. What the HTTP config server will
 *                                  answer with, and what a buyer can run
 *                                  before rebooting.
 *   SIGHUP                         reload the config
 *
 * A reload that fails leaves the running table alone and logs why, so a
 * bad edit degrades to "nothing changed" rather than to a dead device.
 */

#include "siltsim.h"

#include <errno.h>
#include <math.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#define TICK_MS 100

static const char *config_path = "/etc/silt-sim.json";
static volatile sig_atomic_t running = 1;
static volatile sig_atomic_t reload;

static void on_stop(int sig) { (void)sig; running = 0; }
static void on_hup(int sig)  { (void)sig; reload = 1; }

static double simulate(const struct siltsim_tag *t, uint64_t now_ms, double prev)
{
	double span = t->sim_max - t->sim_min;
	double phase;

	if (t->sim_period_ms == 0)
		return prev;
	phase = (double)(now_ms % t->sim_period_ms) / (double)t->sim_period_ms;

	switch (t->sim) {
	case SILTSIM_SIM_SINE:
		return t->sim_min + span * 0.5 * (1.0 - cos(2.0 * M_PI * phase));
	case SILTSIM_SIM_RAMP:
		return t->sim_min + span * phase;
	case SILTSIM_SIM_TOGGLE:
		return phase < 0.5 ? 0.0 : 1.0;
	case SILTSIM_SIM_COUNTER: {
		double v = prev + t->sim_step;

		if (span > 0 && v > t->sim_max)
			v = t->sim_min;
		return v;
	}
	case SILTSIM_SIM_WALK: {
		double step = ((double)rand() / RAND_MAX - 0.5) * t->sim_step;
		double v = prev + step;

		if (v < t->sim_min)
			v = t->sim_min;
		if (v > t->sim_max)
			v = t->sim_max;
		return v;
	}
	default:
		return prev;
	}
}

static int load_and_publish(struct siltsim_table *shm, const char *why)
{
	static struct siltsim_table fresh;   /* large: not on the stack */
	char err[SILTSIM_ERR_LEN] = "";

	if (siltsim_load_file(config_path, &fresh, err, sizeof(err)) != 0) {
		fprintf(stderr, "silt-simd: %s: %s\n", config_path, err);
		return -1;
	}

	siltsim_publish(shm, &fresh);
	fprintf(stderr, "silt-simd: %s \"%s\", %u tag(s), generation %u (%s)\n",
		shm->generation == 1 ? "serving" : "reloaded",
		shm->device, shm->tag_count, shm->generation, why);
	return 0;
}

int main(int argc, char **argv)
{
	uint64_t last_tick[SILTSIM_MAX_TAGS] = { 0 };
	char err[SILTSIM_ERR_LEN] = "";
	struct siltsim_table *shm;
	int foreground = 0, check_only = 0, opt;

	while ((opt = getopt(argc, argv, "c:fth")) != -1) {
		switch (opt) {
		case 'c': config_path = optarg; break;
		case 'f': foreground = 1; break;
		case 't': check_only = 1; break;
		default:
			fprintf(stderr, "usage: %s [-c config] [-f] [-t]\n", argv[0]);
			return opt == 'h' ? EXIT_SUCCESS : EXIT_FAILURE;
		}
	}
	(void)foreground;   /* the init script backgrounds us */

	if (check_only) {
		static struct siltsim_table check;
		char cerr[SILTSIM_ERR_LEN] = "";
		uint32_t i;

		if (siltsim_load_file(config_path, &check, cerr, sizeof(cerr)) != 0) {
			fprintf(stderr, "%s: %s\n", config_path, cerr);
			return EXIT_FAILURE;
		}
		printf("ok  \"%s\", %u tag(s)\n", check.device, check.tag_count);
		for (i = 0; i < check.tag_count; i++) {
			const struct siltsim_tag *t = &check.tag[i];

			printf("    %-16s %s%s%s\n", t->name,
			       t->mb_addr >= 0 ? "modbus " : "",
			       t->opcua_node[0] ? "opcua " : "",
			       t->can_id >= 0 ? "can" : "");
		}
		return EXIT_SUCCESS;
	}

	signal(SIGINT, on_stop);
	signal(SIGTERM, on_stop);
	signal(SIGHUP, on_hup);
	srand((unsigned)time(NULL));

	shm = siltsim_create(err, sizeof(err));
	if (!shm) {
		fprintf(stderr, "silt-simd: %s\n", err);
		return EXIT_FAILURE;
	}
	shm->generation = 0;
	shm->tag_count = 0;

	if (load_and_publish(shm, "start") != 0)
		return EXIT_FAILURE;

	while (running) {
		uint64_t now = siltsim_now_ms();
		uint32_t i;

		if (reload) {
			reload = 0;
			load_and_publish(shm, "SIGHUP");   /* failure keeps the old table */
		}

		for (i = 0; i < shm->tag_count; i++) {
			struct siltsim_tag *t = &shm->tag[i];

			if (t->sim == SILTSIM_SIM_NONE)
				continue;
			/* COUNTER steps once per period; the rest are continuous. */
			if (t->sim == SILTSIM_SIM_COUNTER) {
				if (now - last_tick[i] < t->sim_period_ms)
					continue;
				last_tick[i] = now;
			}
			siltsim_set(t, simulate(t, now, siltsim_get(t)));
		}

		usleep(TICK_MS * 1000);
	}

	fprintf(stderr, "silt-simd: stopping\n");
	return EXIT_SUCCESS;
}
