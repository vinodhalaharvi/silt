/*
 * siltsim - JSON config, validation, and the shared table.
 *
 * The validator is the point of this file. It runs before anything is
 * published, and it is linked into both the sim core and the HTTP config
 * server, so a bad config is refused with a reason rather than half
 * applied. The rules it enforces are the ones a Modbus or OPC UA client
 * would otherwise discover as silence or a wrong number:
 *
 *   - no two tags with the same name
 *   - no two tags on the same Modbus address in the same table
 *   - coils and discrete inputs carry booleans only
 *   - a tag is simulated or writable, never both, so each value has
 *     exactly one writer
 *   - a simulated range that would overflow a 16-bit register at its
 *     scale is a mistake, not a runtime surprise
 */

#include "siltsim.h"

#include <cjson/cJSON.h>

#include <fcntl.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#define ERR(...) do { if (err) snprintf(err, errlen, __VA_ARGS__); } while (0)

/* The generic __atomic_load/store take any type, unlike the _n forms,
 * which are integer and pointer only. A double is 8 bytes and aligned, so
 * these compile to a single load or store on aarch64. */
double siltsim_get(const struct siltsim_tag *t)
{
	double v;

	__atomic_load((double *)&t->value, &v, __ATOMIC_RELAXED);
	return v;
}

void siltsim_set(struct siltsim_tag *t, double v)
{
	__atomic_store(&t->value, &v, __ATOMIC_RELAXED);
}

uint64_t siltsim_now_ms(void)
{
	struct timespec ts;

	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (uint64_t)ts.tv_sec * 1000 + (uint64_t)(ts.tv_nsec / 1000000);
}

static int copy_str(char *dst, size_t cap, const cJSON *v)
{
	size_t n;

	if (!cJSON_IsString(v) || !v->valuestring)
		return -1;
	n = strlen(v->valuestring);
	if (n == 0 || n >= cap)
		return -1;
	memcpy(dst, v->valuestring, n + 1);
	return 0;
}

static double num(const cJSON *o, const char *key, double dflt)
{
	const cJSON *v = cJSON_GetObjectItemCaseSensitive(o, key);

	return cJSON_IsNumber(v) ? v->valuedouble : dflt;
}

static int parse_sim(const cJSON *sim, struct siltsim_tag *t,
		     char *err, size_t errlen)
{
	static const struct { const char *name; uint32_t kind; } kinds[] = {
		{ "sine", SILTSIM_SIM_SINE },     { "ramp", SILTSIM_SIM_RAMP },
		{ "toggle", SILTSIM_SIM_TOGGLE }, { "counter", SILTSIM_SIM_COUNTER },
		{ "walk", SILTSIM_SIM_WALK },
	};
	size_t i;

	for (i = 0; i < sizeof(kinds) / sizeof(kinds[0]); i++) {
		const cJSON *p = cJSON_GetObjectItemCaseSensitive(sim, kinds[i].name);

		if (!p)
			continue;
		if (!cJSON_IsObject(p)) {
			ERR("tag \"%s\": sim.%s must be an object", t->name, kinds[i].name);
			return -1;
		}
		t->sim = kinds[i].kind;
		t->sim_min = num(p, "min", 0);
		t->sim_max = num(p, "max", 1);
		t->sim_step = num(p, "step", 1);
		t->sim_period_ms = (uint32_t)(num(p, "period_s", 10) * 1000);
		if (t->sim_period_ms < 10)
			t->sim_period_ms = 10;
		if (t->sim_max < t->sim_min) {
			ERR("tag \"%s\": sim max is below min", t->name);
			return -1;
		}
		return 0;
	}

	ERR("tag \"%s\": sim names no known behaviour "
	    "(sine, ramp, toggle, counter, walk)", t->name);
	return -1;
}

static int parse_modbus(const cJSON *mb, struct siltsim_tag *t,
			char *err, size_t errlen)
{
	static const struct { const char *name; uint32_t table; } tables[] = {
		{ "holding", SILTSIM_MB_HOLDING },   { "input", SILTSIM_MB_INPUT },
		{ "coil", SILTSIM_MB_COIL },         { "discrete", SILTSIM_MB_DISCRETE },
	};
	size_t i;

	t->mb_scale = num(mb, "scale", 1);
	if (t->mb_scale == 0) {
		ERR("tag \"%s\": modbus scale of 0 would erase the value", t->name);
		return -1;
	}

	for (i = 0; i < sizeof(tables) / sizeof(tables[0]); i++) {
		const cJSON *a = cJSON_GetObjectItemCaseSensitive(mb, tables[i].name);

		if (!a)
			continue;
		if (!cJSON_IsNumber(a) || a->valuedouble < 0 || a->valuedouble > 65535) {
			ERR("tag \"%s\": modbus %s address must be 0..65535",
			    t->name, tables[i].name);
			return -1;
		}
		t->mb_table = tables[i].table;
		t->mb_addr = (int32_t)a->valuedouble;

		if ((t->mb_table == SILTSIM_MB_COIL ||
		     t->mb_table == SILTSIM_MB_DISCRETE) && t->type != SILTSIM_BOOL) {
			ERR("tag \"%s\": %s carries one bit, so the tag must be bool",
			    t->name, tables[i].name);
			return -1;
		}
		return 0;
	}

	ERR("tag \"%s\": modbus names no table "
	    "(holding, input, coil, discrete)", t->name);
	return -1;
}

static int parse_can(const cJSON *can, struct siltsim_tag *t,
		     char *err, size_t errlen)
{
	const cJSON *id = cJSON_GetObjectItemCaseSensitive(can, "id");
	long v;

	if (cJSON_IsString(id) && id->valuestring)
		v = strtol(id->valuestring, NULL, 0);   /* "0x100" */
	else if (cJSON_IsNumber(id))
		v = (long)id->valuedouble;
	else {
		ERR("tag \"%s\": can.id must be a number or a string like \"0x100\"",
		    t->name);
		return -1;
	}
	if (v < 0 || v > 0x1FFFFFFF) {
		ERR("tag \"%s\": can.id is outside the 29-bit range", t->name);
		return -1;
	}

	t->can_id = (int32_t)v;
	t->can_byte = (uint32_t)num(can, "byte", 0);
	t->can_len = (uint32_t)num(can, "len", 2);
	t->can_scale = num(can, "scale", 1);
	t->can_period_ms = (uint32_t)(num(can, "period_s", 0.1) * 1000);
	if (t->can_period_ms < 10)
		t->can_period_ms = 10;

	if (t->can_len != 1 && t->can_len != 2 && t->can_len != 4) {
		ERR("tag \"%s\": can.len must be 1, 2 or 4", t->name);
		return -1;
	}
	if (t->can_byte + t->can_len > 8) {
		ERR("tag \"%s\": can byte %u plus length %u runs past the 8-byte frame",
		    t->name, t->can_byte, t->can_len);
		return -1;
	}
	if (t->can_scale == 0) {
		ERR("tag \"%s\": can scale of 0 would erase the value", t->name);
		return -1;
	}
	return 0;
}

/* Would the simulated range still fit a 16-bit register once scaled? */
static int check_mb_range(const struct siltsim_tag *t, char *err, size_t errlen)
{
	double lo, hi;

	if (t->mb_table != SILTSIM_MB_HOLDING && t->mb_table != SILTSIM_MB_INPUT)
		return 0;
	if (t->sim == SILTSIM_SIM_NONE)
		return 0;

	lo = t->sim_min * t->mb_scale;
	hi = t->sim_max * t->mb_scale;
	if (lo < -32768 || hi > 65535) {
		ERR("tag \"%s\": %g..%g scaled by %g does not fit a 16-bit register",
		    t->name, t->sim_min, t->sim_max, t->mb_scale);
		return -1;
	}
	return 0;
}

static int parse_tag(const cJSON *j, struct siltsim_tag *t,
		     char *err, size_t errlen)
{
	const cJSON *v;

	memset(t, 0, sizeof(*t));
	t->mb_addr = -1;
	t->can_id = -1;
	t->mb_scale = 1;
	t->can_scale = 1;

	if (copy_str(t->name, sizeof(t->name), cJSON_GetObjectItemCaseSensitive(j, "name")) != 0) {
		ERR("a tag has no name, or one longer than %d characters",
		    SILTSIM_NAME_LEN - 1);
		return -1;
	}

	v = cJSON_GetObjectItemCaseSensitive(j, "unit");
	if (v && copy_str(t->unit, sizeof(t->unit), v) != 0) {
		ERR("tag \"%s\": unit must be a short string", t->name);
		return -1;
	}

	v = cJSON_GetObjectItemCaseSensitive(j, "type");
	if (!v)
		t->type = SILTSIM_FLOAT;
	else if (cJSON_IsString(v) && !strcmp(v->valuestring, "float"))
		t->type = SILTSIM_FLOAT;
	else if (cJSON_IsString(v) && !strcmp(v->valuestring, "int"))
		t->type = SILTSIM_INT;
	else if (cJSON_IsString(v) && !strcmp(v->valuestring, "bool"))
		t->type = SILTSIM_BOOL;
	else {
		ERR("tag \"%s\": type must be float, int or bool", t->name);
		return -1;
	}

	v = cJSON_GetObjectItemCaseSensitive(j, "writable");
	t->writable = cJSON_IsTrue(v) ? 1 : 0;

	v = cJSON_GetObjectItemCaseSensitive(j, "sim");
	if (v) {
		if (!cJSON_IsObject(v)) {
			ERR("tag \"%s\": sim must be an object", t->name);
			return -1;
		}
		if (parse_sim(v, t, err, errlen) != 0)
			return -1;
	}

	if (t->writable && t->sim != SILTSIM_SIM_NONE) {
		ERR("tag \"%s\": writable and simulated at once, so two writers "
		    "would fight over it; drop one", t->name);
		return -1;
	}

	t->value = num(j, "initial", 0);

	v = cJSON_GetObjectItemCaseSensitive(j, "modbus");
	if (v) {
		if (!cJSON_IsObject(v)) {
			ERR("tag \"%s\": modbus must be an object", t->name);
			return -1;
		}
		if (parse_modbus(v, t, err, errlen) != 0)
			return -1;
		if (check_mb_range(t, err, errlen) != 0)
			return -1;
	}

	v = cJSON_GetObjectItemCaseSensitive(j, "opcua");
	if (v && copy_str(t->opcua_node, sizeof(t->opcua_node), v) != 0) {
		ERR("tag \"%s\": opcua must be a node name under %d characters",
		    t->name, SILTSIM_NODE_LEN - 1);
		return -1;
	}

	v = cJSON_GetObjectItemCaseSensitive(j, "can");
	if (v) {
		if (!cJSON_IsObject(v)) {
			ERR("tag \"%s\": can must be an object", t->name);
			return -1;
		}
		if (parse_can(v, t, err, errlen) != 0)
			return -1;
	}

	if (t->mb_addr < 0 && t->opcua_node[0] == '\0' && t->can_id < 0) {
		ERR("tag \"%s\": no protocol carries it, so nothing could read it",
		    t->name);
		return -1;
	}
	return 0;
}

static int check_unique(const struct siltsim_table *tb, char *err, size_t errlen)
{
	uint32_t i, k;

	for (i = 0; i < tb->tag_count; i++) {
		for (k = i + 1; k < tb->tag_count; k++) {
			const struct siltsim_tag *a = &tb->tag[i], *b = &tb->tag[k];

			if (!strcmp(a->name, b->name)) {
				ERR("two tags are named \"%s\"", a->name);
				return -1;
			}
			if (a->mb_addr >= 0 && a->mb_addr == b->mb_addr &&
			    a->mb_table == b->mb_table) {
				ERR("tags \"%s\" and \"%s\" share Modbus address %d",
				    a->name, b->name, a->mb_addr);
				return -1;
			}
			if (a->opcua_node[0] && !strcmp(a->opcua_node, b->opcua_node)) {
				ERR("tags \"%s\" and \"%s\" share OPC UA node \"%s\"",
				    a->name, b->name, a->opcua_node);
				return -1;
			}
			if (a->can_id >= 0 && a->can_id == b->can_id &&
			    a->can_byte < b->can_byte + b->can_len &&
			    b->can_byte < a->can_byte + a->can_len) {
				ERR("tags \"%s\" and \"%s\" overlap in CAN frame 0x%X",
				    a->name, b->name, a->can_id);
				return -1;
			}
		}
	}
	return 0;
}

int siltsim_load_json(const char *json, size_t len, struct siltsim_table *out,
		      char *err, size_t errlen)
{
	const cJSON *tags, *jt, *dev;
	cJSON *root;
	uint32_t n = 0;

	root = cJSON_ParseWithLength(json, len);
	if (!root) {
		const char *at = cJSON_GetErrorPtr();
		char near[33] = "";
		size_t i;

		/* Quote the text it stopped on, on one line: this message goes
		 * back over HTTP and into a log. */
		for (i = 0; at && i < sizeof(near) - 1 && at[i]; i++)
			near[i] = (at[i] >= ' ' && at[i] < 127) ? at[i] : ' ';
		ERR("not valid JSON%s%s", near[0] ? ", near: " : "", near);
		return -1;
	}

	memset(out, 0, sizeof(*out));
	out->magic = SILTSIM_MAGIC;
	out->abi = SILTSIM_ABI;

	dev = cJSON_GetObjectItemCaseSensitive(root, "device");
	if (!dev)
		strcpy(out->device, "device");
	else if (copy_str(out->device, sizeof(out->device), dev) != 0) {
		ERR("device must be a name under %d characters", SILTSIM_NAME_LEN - 1);
		goto fail;
	}

	tags = cJSON_GetObjectItemCaseSensitive(root, "tags");
	if (!cJSON_IsArray(tags)) {
		ERR("no tags array");
		goto fail;
	}

	cJSON_ArrayForEach(jt, tags) {
		if (n == SILTSIM_MAX_TAGS) {
			ERR("more than %d tags", SILTSIM_MAX_TAGS);
			goto fail;
		}
		if (!cJSON_IsObject(jt)) {
			ERR("tag %u is not an object", n);
			goto fail;
		}
		if (parse_tag(jt, &out->tag[n], err, errlen) != 0)
			goto fail;
		n++;
	}

	if (n == 0) {
		ERR("the tags array is empty");
		goto fail;
	}
	out->tag_count = n;

	if (check_unique(out, err, errlen) != 0)
		goto fail;

	cJSON_Delete(root);
	return 0;

fail:
	cJSON_Delete(root);
	return -1;
}

int siltsim_load_file(const char *path, struct siltsim_table *out,
		      char *err, size_t errlen)
{
	long size;
	char *buf;
	FILE *f;
	int rc;

	f = fopen(path, "rb");
	if (!f) {
		ERR("cannot open %s", path);
		return -1;
	}
	if (fseek(f, 0, SEEK_END) != 0 || (size = ftell(f)) < 0) {
		ERR("cannot read %s", path);
		fclose(f);
		return -1;
	}
	rewind(f);
	if (size > 1024 * 1024) {
		ERR("%s is larger than 1MB", path);
		fclose(f);
		return -1;
	}

	buf = malloc((size_t)size + 1);
	if (!buf) {
		ERR("out of memory reading %s", path);
		fclose(f);
		return -1;
	}
	if (fread(buf, 1, (size_t)size, f) != (size_t)size) {
		ERR("short read on %s", path);
		free(buf);
		fclose(f);
		return -1;
	}
	fclose(f);
	buf[size] = '\0';

	rc = siltsim_load_json(buf, (size_t)size, out, err, errlen);
	free(buf);
	return rc;
}

static struct siltsim_table *map_shm(int flags, int prot, mode_t mode,
				     int create, char *err, size_t errlen)
{
	struct siltsim_table *t;
	int fd;

	fd = open(SILTSIM_PATH, flags, mode);
	if (fd == -1) {
		ERR("%s: %s", SILTSIM_PATH,
		    create ? "cannot create" : "not there yet (is silt-simd running?)");
		return NULL;
	}
	if (create && ftruncate(fd, sizeof(struct siltsim_table)) != 0) {
		ERR("cannot size %s", SILTSIM_PATH);
		close(fd);
		return NULL;
	}

	t = mmap(NULL, sizeof(struct siltsim_table), prot, MAP_SHARED, fd, 0);
	close(fd);
	if (t == MAP_FAILED) {
		ERR("cannot map %s", SILTSIM_PATH);
		return NULL;
	}
	return t;
}

struct siltsim_table *siltsim_create(char *err, size_t errlen)
{
	return map_shm(O_RDWR | O_CREAT, PROT_READ | PROT_WRITE, 0644, 1, err, errlen);
}

struct siltsim_table *siltsim_attach(int writable, char *err, size_t errlen)
{
	struct siltsim_table *t;

	t = map_shm(writable ? O_RDWR : O_RDONLY,
		    writable ? (PROT_READ | PROT_WRITE) : PROT_READ,
		    0, 0, err, errlen);
	if (!t)
		return NULL;

	if (t->magic != SILTSIM_MAGIC || t->abi != SILTSIM_ABI) {
		ERR("%s holds a table this build does not understand", SILTSIM_PATH);
		munmap(t, sizeof(*t));
		return NULL;
	}
	return t;
}

void siltsim_publish(struct siltsim_table *shm, const struct siltsim_table *fresh)
{
	uint32_t i, k;

	/* Carry over what a client has written, so a reload does not reset a
	 * setpoint someone set over Modbus. */
	for (i = 0; i < fresh->tag_count; i++) {
		struct siltsim_tag *nt = (struct siltsim_tag *)&fresh->tag[i];

		for (k = 0; k < shm->tag_count && k < SILTSIM_MAX_TAGS; k++) {
			const struct siltsim_tag *ot = &shm->tag[k];

			if (nt->writable && ot->writable &&
			    nt->type == ot->type && !strcmp(nt->name, ot->name)) {
				nt->value = siltsim_get(ot);
				break;
			}
		}
	}

	/* Readers key off the generation, so land the contents first and make
	 * the new generation visible last. */
	shm->tag_count = 0;
	__atomic_thread_fence(__ATOMIC_RELEASE);

	memcpy(shm->device, fresh->device, sizeof(shm->device));
	memcpy(shm->tag, fresh->tag, sizeof(shm->tag));
	shm->magic = SILTSIM_MAGIC;
	shm->abi = SILTSIM_ABI;
	shm->started_ms = siltsim_now_ms();
	shm->tag_count = fresh->tag_count;

	__atomic_thread_fence(__ATOMIC_RELEASE);
	__atomic_add_fetch(&shm->generation, 1, __ATOMIC_RELEASE);
}
