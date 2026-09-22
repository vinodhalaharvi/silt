/*
 * Tests for the CAN frame packing, which is the part of silt-cand that has
 * no socket in it: several tags sharing one id, big-endian fields at their
 * own offsets, the frame length being the highest byte any tag reaches,
 * and a received frame reaching writable tags only.
 *
 *   cc -I ../silt-sim -DSILTCAN_NO_MAIN silt-cand-test.c silt-cand.c siltsim.o
 */

#include "siltsim.h"

#include <stdio.h>
#include <string.h>

uint8_t siltcan_build(const struct siltsim_table *tb, int32_t can_id, uint8_t *data);
void siltcan_apply(struct siltsim_table *tb, int32_t can_id,
		   const uint8_t *data, uint8_t dlc);

static int failures;

static void check(const char *what, int ok)
{
	printf("%-58s %s\n", what, ok ? "ok" : "FAILED");
	if (!ok)
		failures++;
}

static struct siltsim_tag *add(struct siltsim_table *tb, const char *name,
			       int32_t id, uint32_t byte, uint32_t len,
			       double scale, uint32_t writable)
{
	struct siltsim_tag *t = &tb->tag[tb->tag_count++];

	memset(t, 0, sizeof(*t));
	snprintf(t->name, sizeof(t->name), "%s", name);
	t->can_id = id;
	t->can_byte = byte;
	t->can_len = len;
	t->can_scale = scale;
	t->can_period_ms = 100;
	t->mb_addr = -1;
	t->writable = writable;
	return t;
}

int main(void)
{
	static struct siltsim_table tb;
	struct siltsim_tag *temp, *press, *rpm, *sp;
	uint8_t data[8];
	uint8_t dlc;

	tb.magic = SILTSIM_MAGIC;

	/* The default pump's 0x100: three tags in one frame. */
	temp  = add(&tb, "temperature", 0x100, 0, 2, 10, 0);
	press = add(&tb, "pressure",    0x100, 2, 2, 100, 0);
	rpm   = add(&tb, "rpm",         0x100, 4, 2, 1, 0);
	sp    = add(&tb, "setpoint",    0x200, 0, 2, 10, 1);

	siltsim_set(temp, 54.3);
	siltsim_set(press, 2.5);
	siltsim_set(rpm, 1500);
	siltsim_set(sp, 60);

	dlc = siltcan_build(&tb, 0x100, data);
	check("frame length is the highest byte any tag reaches", dlc == 6);
	check("54.3 at scale 10 is 0x021F big-endian at byte 0",
	      data[0] == 0x02 && data[1] == 0x1F);
	check("2.5 at scale 100 is 0x00FA at byte 2",
	      data[2] == 0x00 && data[3] == 0xFA);
	check("1500 at scale 1 is 0x05DC at byte 4",
	      data[4] == 0x05 && data[5] == 0xDC);
	check("bytes no tag claims stay zero", data[6] == 0 && data[7] == 0);

	/* A one-byte field, and a value that would overflow it. */
	add(&tb, "fault", 0x101, 0, 1, 1, 0);
	siltsim_set(&tb.tag[tb.tag_count - 1], 1);
	dlc = siltcan_build(&tb, 0x101, data);
	check("a one-byte boolean gives a one-byte frame", dlc == 1 && data[0] == 1);

	siltsim_set(temp, 99999);
	siltcan_build(&tb, 0x100, data);
	check("a value too large for its field saturates rather than wrapping",
	      data[0] == 0xFF && data[1] == 0xFF);

	siltsim_set(temp, -5);
	siltcan_build(&tb, 0x100, data);
	check("-5 at scale 10 is two's complement 0xFFCE",
	      data[0] == 0xFF && data[1] == 0xCE);

	check("an id no tag uses gives no frame", siltcan_build(&tb, 0x7AA, data) == 0);

	/* Receiving. */
	memset(data, 0, sizeof(data));
	data[0] = 0x02; data[1] = 0xEE;                 /* 750 -> 75.0 */
	siltcan_apply(&tb, 0x200, data, 2);
	check("a received frame writes a writable tag", siltsim_get(sp) == 75.0);

	siltsim_set(temp, 54.3);
	data[0] = 0x01; data[1] = 0x00;
	siltcan_apply(&tb, 0x100, data, 6);
	check("a received frame leaves simulated tags alone", siltsim_get(temp) == 54.3);

	siltsim_set(sp, 60);
	siltcan_apply(&tb, 0x200, data, 1);             /* too short to carry it */
	check("a frame too short for a tag does not write it", siltsim_get(sp) == 60);

	printf("\n%s\n", failures ? "FAILURES" : "all ok");
	return failures ? 1 : 0;
}
