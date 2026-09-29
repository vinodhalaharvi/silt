# CI and downloads

Two workflows, because the work costs two very different amounts.

    push / pull request
          │
          ├──► ci.yml            GitHub runners, ~7 min, every commit, free
          │      gofmt, vet, unit tests
          │      importer and fixpoint against a real Buildroot tree
          │      the evaluator against Buildroot's own conf binary
          │      CONFIG_* claims against a real 6.12 kernel
          │      every image composed and verified
          │
          └──► build.yml         gated: manual, a v* tag, or nightly
                 start br2-builder
                 ci/ci-build.sh <commit> [image...]
                 collect, compress, copy down
                 bucket + run artifact
                 stop br2-builder

## What a green ci.yml actually asserts

Not "it compiles". On the last run:

    290 defconfigs, 0 rejected by the model
    25 of 25 compositions keep every stated line
    2134 stated buildroot lines checked against kbuild's .config
    55 images, 0 mismatches
    55 images, 573 CONFIG_ claims checked
    10 defconfigs, 10 identical to kbuild's .config
    20 perturbed configs, 20 identical to kbuild's .config

That is silt's prediction checked against Buildroot's own `conf` binary and
against a real kernel's Kconfig, on every commit, in seven minutes, without
building anything.

## What gets built, and why it is not a list

An image says:

    (ci build)   build it and keep the artifact
    (ci boot)    build it and boot it
                 absent → check only

and the workflow asks:

    $ silt images --ci build
    images/cm5-io-appliance.sx
    images/imx8mp-evk-bringup.sx
    images/raspberrypi3-64-bringup.sx
    images/raspberrypi3-64-garage.sx
    images/raspberrypi5-bringup.sx

A list of names in a workflow file is right until someone adds an image and
forgets. The image is the one place that cannot drift from itself.

Fifty-five images are checked on every commit; five are built, and only when
asked.

## Running a build

    gh workflow run build.yml -f image=images/qemu-arm-appliance.sx
    gh run watch

With no image, it builds everything marked `(ci build)`.

## Where the images end up

The builder gathers what it produced; the runner copies it down and sends it
outward. Nothing leaves the VM by the VM's own hand, so the VM needs no cloud
permission at all.

**The run's artifacts** — click and download from the Actions page, no console
and no credentials. Capped at 2GB a file and they expire.

**The bucket** — every image from every build, addressed by commit:

    gs://silt-images-<project>/<sha>/qemu-arm-appliance/
      Image.zst
      rootfs.ext4.zst
      defconfig
      predicted.config
      manifest
      README.txt

Images are zstd-compressed; the three text files are not, because they are read
rather than written to a card. `README.txt` says how to use what is beside it.

The `manifest` and `predicted.config` are why a download page over this bucket
can say what each image *is* rather than only what it is called: the exact
symbols, the solution hash, the build time.

## The builder

One instance, `br2-builder`, because the store is tens of gigabytes of warm
output trees and a tree is only worth anything where it already is. A hosted
runner would start cold every time and a one-minute build would take half an
hour.

It is started and stopped by the workflow. On failure it is left running, with
the commands to look at it written into the job summary - a failed build is
exactly when you want to log in, and shutting down takes the evidence with it.

CI logs in as `runner`, an account whose home holds a clone and a scratch
directory and nothing else. Not as a person: a job running as a person has that
person's ssh keys and cloud credentials, and the people who can put code into
that job include whoever controls the tags of the actions it uses.

## Authentication without a key

Workload Identity Federation. GitHub signs a short-lived token saying which
repository is asking; GCP is configured to trust that issuer for that repository
only, and mints a credential good for an hour. There is no secret in the
repository to leak, and a pull request from a fork cannot obtain one.

The setup is in [docs/CI.md](https://github.com/vinodhalaharvi/silt/blob/main/docs/CI.md),
with the exact `gcloud` invocations.
