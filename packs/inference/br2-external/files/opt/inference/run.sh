#!/bin/sh
# A stand-in for the real agent.
#
# It checks the runtime and the model are both present and reports what it
# found, once, on stdout — which is enough to prove the image is assembled
# correctly. Replacing this with something that actually infers is the
# customer's application, and deliberately not Silt's business: the moment
# this pack ships an inference program, it is a deployment tool rather than a
# configuration one.

set -e
. /etc/default/inference

if [ ! -f "$MODEL" ]; then
    echo "inference: no model at $MODEL"
    echo "inference: the image carries a placeholder; drop a real model in the pack"
    exit 1
fi

echo "inference: runtime $(ls /usr/lib/libtensorflow-lite* 2>/dev/null | head -1)"
echo "inference: model $MODEL ($(stat -c %s "$MODEL") bytes)"
echo "inference: source $SOURCE, every $EVERY frames"
echo "inference: ready"

# Nothing to do yet; hold the service open so its state is visible.
exec sleep infinity
