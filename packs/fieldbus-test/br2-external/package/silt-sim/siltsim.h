/*
 * siltsim - the shared tag table.
 *
 * One simulated device, several protocol daemons. The sim core parses the
 * JSON, validates it, and writes this table into /dev/shm/silt-sim. Every
 * protocol daemon maps the same file and reads the values and its own
 * mapping straight out of memory: no socket, no second parser, and no
 * second place where "holding register 0 is the temperature" is written
 * down.
 *
 * Rules that keep it lock-free:
 *
 *   - Every field is fixed size. No pointers: an address in one process
 *     means nothing in another.
 *   - Each value is a double, 8-byte aligned, read and written atomically.
 *   - Exactly one writer per value. The sim core writes tags that have a
 *     simulated behaviour; a protocol daemon writes only tags marked
 *     writable, on behalf of a client. The two sets never overlap, which
 *     the validator enforces.
 *   - The table's shape changes only when the config is reloaded, and then
 *     the generation counter changes. A daemon that sees a new generation
 *     rebuilds its own lookup tables.
 */

#ifndef SILTSIM_H
#define SILTSIM_H

#include <stddef.h>
#include <stdint.h>

#define SILTSIM_PATH        "/dev/shm/silt-sim"
#define SILTSIM_MAGIC       0x53494C54u /* "SILT" */
#define SILTSIM_ABI         1u
#define SILTSIM_MAX_TAGS    256
#define SILTSIM_NAME_LEN    32
#define SILTSIM_UNIT_LEN    16
#define SILTSIM_NODE_LEN    64
#define SILTSIM_ERR_LEN     256

/* What a value is. Everything is carried as a double; the type says how a
 * protocol should present it. */
enum siltsim_type {
	SILTSIM_FLOAT = 0,
	SILTSIM_INT   = 1,
	SILTSIM_BOOL  = 2
};

/* How the sim core moves a value. NONE means nobody simulates it: either a
 * constant, or a tag a client writes. */
enum siltsim_sim {
	SILTSIM_SIM_NONE    = 0,
	SILTSIM_SIM_SINE    = 1,  /* min..max, smooth */
	SILTSIM_SIM_RAMP    = 2,  /* min..max, sawtooth */
	SILTSIM_SIM_TOGGLE  = 3,  /* 0/1 every period */
	SILTSIM_SIM_COUNTER = 4,  /* +step every period, wraps at max */
	SILTSIM_SIM_WALK    = 5   /* random walk within min..max */
};

/* Which Modbus table a tag appears in. NONE means it has no Modbus
 * address, which is fine: a tag may be OPC UA or CAN only. */
enum siltsim_mb_table {
	SILTSIM_MB_NONE     = 0,
	SILTSIM_MB_HOLDING  = 1,
	SILTSIM_MB_INPUT    = 2,
	SILTSIM_MB_COIL     = 3,
	SILTSIM_MB_DISCRETE = 4
};

struct siltsim_tag {
	char     name[SILTSIM_NAME_LEN];
	char     unit[SILTSIM_UNIT_LEN];

	uint32_t type;          /* enum siltsim_type */
	uint32_t writable;      /* a client may write it; the sim core will not */

	uint32_t sim;           /* enum siltsim_sim */
	uint32_t sim_period_ms; /* one cycle, or one step for COUNTER */
	double   sim_min;
	double   sim_max;
	double   sim_step;

	/* Modbus. A 16-bit register carries value * scale, rounded. */
	uint32_t mb_table;      /* enum siltsim_mb_table */
	int32_t  mb_addr;       /* -1 when absent */
	double   mb_scale;

	/* OPC UA and CAN: read by their daemons, unused by the Modbus one. */
	char     opcua_node[SILTSIM_NODE_LEN];  /* empty when absent */
	int32_t  can_id;        /* -1 when absent */
	uint32_t can_byte;
	uint32_t can_len;       /* 1, 2 or 4 bytes, big-endian in the frame */
	uint32_t can_period_ms;
	double   can_scale;

	uint32_t pad;
	double   value;         /* the live value; one writer, atomic access */
};

struct siltsim_table {
	uint32_t magic;
	uint32_t abi;
	uint32_t generation;    /* bumped on every reload */
	uint32_t tag_count;
	char     device[SILTSIM_NAME_LEN];
	uint64_t started_ms;
	struct siltsim_tag tag[SILTSIM_MAX_TAGS];
};

/* Read a value. Safe from any process, at any time. */
double siltsim_get(const struct siltsim_tag *t);

/* Write a value. Only the one writer for that tag may call it. */
void siltsim_set(struct siltsim_tag *t, double v);

/* Milliseconds from a monotonic clock. */
uint64_t siltsim_now_ms(void);

/*
 * Parse and validate a config file into a table. Nothing is changed unless
 * the whole file is valid, so a bad POST or a bad edit leaves the running
 * device alone. Returns 0, or -1 with the reason in err.
 */
int siltsim_load_file(const char *path, struct siltsim_table *out,
		      char *err, size_t errlen);
int siltsim_load_json(const char *json, size_t len, struct siltsim_table *out,
		      char *err, size_t errlen);

/* Create or replace the shared table (the sim core). */
struct siltsim_table *siltsim_create(char *err, size_t errlen);

/* Map the shared table. writable lets a daemon write client-written tags.
 * Returns NULL if the sim core has not created it yet. */
struct siltsim_table *siltsim_attach(int writable, char *err, size_t errlen);

/* Publish a freshly loaded table into shared memory under a new
 * generation. Values of tags that kept their name and type are carried
 * over, so a reload does not reset a setpoint a client has written. */
void siltsim_publish(struct siltsim_table *shm, const struct siltsim_table *fresh);

#endif /* SILTSIM_H */
