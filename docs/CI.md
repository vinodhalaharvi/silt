# CI

Two kinds of work, on two kinds of machine, because they cost different
amounts. Checking an image is a second and catches real bugs. Building one is
twenty minutes on a machine somebody pays for.

```
  push / pull request
        │
        ├──► GitHub-hosted runner              seconds, free, every commit
        │      go build ./cmd/silt && go test ./...
        │      silt fmt --check
        │      silt check --buildroot <buildroot>
        │      silt solve / complete per image
        │         └─ fails here and no VM ever starts
        │
        └──► gated: a label, a tag, or a schedule
                   │
                   ▼
             gcloud compute instances start br2-builder
                   │
                   ▼
             self-hosted runner on the VM
               for i in $(silt images --ci build); do
                 ci/silt-build.sh "$i"
               done
                 hit    → 3s
                 prefix → 1-5 min
                 miss   → 20-30 min
               ~/.cache/silt persists on the disk
                   │
                   ├──► sdcard.img + defconfig + predicted.config → GCS
                   ├──► solution hash and timings → job summary
                   └──► gcloud compute instances stop br2-builder
```

## Why the builder is one particular machine

`~/.cache/silt` is tens of gigabytes of Buildroot output trees, and a tree is
only worth anything where it already is: restoring one somewhere else would
cost more than the build it saves. A GitHub-hosted runner starts cold every
time, so a build that takes a minute here would take half an hour there.

So the builder is `br2-builder` in `us-east1-b`, one instance with a persistent
disk. Stopping it keeps the disk — the store, `~/buildroot`, and
`~/.cache/buildroot-dl` are exactly as they were. Only the ephemeral external
IP changes, which nothing depends on.

Compute stops costing when the instance stops; the disk does not. Pruning old
trees on a schedule (`ci/silt-build.sh --prune N`) keeps that bounded.

## What CI builds, and why it is not a list

Fifty-five images, most of them describing boards nobody has or variants kept
to prove a composition holds. The image itself says what CI should do with it:

```lisp
(ci build)   ;; build it and keep the artifact
(ci boot)    ;; build it and run the boot test
             ;; absent → check only
```

and CI asks rather than being told:

```sh
silt images --ci build     # paths to build
silt images --ci boot
```

A list of image names in a workflow file would be correct until someone adds
an image and forgets, which is the same failure as the hand-maintained
`BR2_EXTERNAL` lines that `silt externals` replaced. The image is the one place
that cannot drift from itself.

## Authentication: no key anywhere

The old way is a service account JSON key in a repository secret. That key is a
password that never expires, and anything that can read the secret — a
compromised action, anyone with repo access — has the account until it is
noticed and rotated.

Workload Identity Federation instead. GitHub signs a short-lived OIDC token
saying which repository and which ref the job is running for; GCP is configured
to trust that issuer for that repository only, and mints an access token good
for an hour.

```
  GitHub Actions job
     │  OIDC token: repo=vinodhalaharvi/silt, ref=refs/heads/main
     ▼
  Workload Identity Pool "github", provider "silt"
     │  attribute-condition: assertion.repository == 'vinodhalaharvi/silt'
     ▼
  service account silt-ci@buildroot-vh-c2
     │  compute.instanceAdmin.v1 on br2-builder, objectAdmin on one bucket
     ▼
  gcloud compute instances start br2-builder
```

No secret exists to leak, and a pull request from a fork cannot start the VM.

### One-time setup

Run once, from a machine already authenticated to the project.

```sh
PROJECT=buildroot-vh-c2
PROJNUM=353644819277
REPO=vinodhalaharvi/silt

gcloud config set project $PROJECT

gcloud services enable \
  iamcredentials.googleapis.com sts.googleapis.com compute.googleapis.com

gcloud iam service-accounts create silt-ci \
  --display-name="silt CI: starts and stops br2-builder"
```

The pool and the provider:

```sh
gcloud iam workload-identity-pools create github \
  --location=global --display-name="GitHub Actions"

gcloud iam workload-identity-pools providers create-oidc silt \
  --location=global --workload-identity-pool=github \
  --display-name="silt repo" \
  --issuer-uri="https://token.actions.githubusercontent.com" \
  --attribute-mapping="google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" \
  --attribute-condition="assertion.repository == '$REPO'"
```

`--attribute-condition` is the line that matters. Without it every GitHub
repository in the world can mint tokens for this account.

Let only that repository impersonate the service account:

```sh
gcloud iam service-accounts add-iam-policy-binding \
  silt-ci@$PROJECT.iam.gserviceaccount.com \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/$PROJNUM/locations/global/workloadIdentityPools/github/attribute.repository/$REPO"
```

Give it the least it needs — one instance, one bucket:

```sh
gcloud compute instances add-iam-policy-binding br2-builder \
  --zone=us-east1-b \
  --member="serviceAccount:silt-ci@$PROJECT.iam.gserviceaccount.com" \
  --role=roles/compute.instanceAdmin.v1

gcloud storage buckets create gs://silt-images-$PROJNUM --location=us-east1
gcloud storage buckets add-iam-policy-binding gs://silt-images-$PROJNUM \
  --member="serviceAccount:silt-ci@$PROJECT.iam.gserviceaccount.com" \
  --role=roles/storage.objectAdmin
```

The two values the workflow needs. Neither is a secret; both are identifiers
and belong in the workflow file:

```
workload_identity_provider:
  projects/353644819277/locations/global/workloadIdentityPools/github/providers/silt
service_account:
  silt-ci@buildroot-vh-c2.iam.gserviceaccount.com
```

## The workflows

`.github/workflows/ci.yml` is the cheap half and predates all of this: gofmt,
vet, unit tests, then the real work - the importer and fixpoint tests against a
cached Buildroot tree, the evaluator checked against Buildroot's own conf
binary, CONFIG_* claims against a real kernel's Kconfig, and `ci/check-images.sh`
over every image. A weekly job imports and round-trips every defconfig
Buildroot ships.

A second workflow was added here and then deleted: it duplicated a weaker
version of that, and called `silt fmt --check`, which is not a flag silt has.
What it had that was worth keeping is one step, now in ci.yml: printing what is
marked `(ci build)` into the job summary, so a pull request that marks an image
shows what it has signed the builder up for.

`.github/workflows/build.yml` is gated: manual dispatch, a `v*` tag, or the
nightly schedule. It authenticates with WIF, starts the instance, checks the
builder's clone out at the commit being built rather than at main, runs
`ci/silt-build.sh` for everything `silt images --ci build` reports, copies the
artifacts to the bucket, and stops the instance in an `always()` step.

It takes an optional image path on manual dispatch, for building one thing
without waiting for the set.

`concurrency: br2-builder` allows one run at a time, because the builder has a
single work directory and a flock to match. Two runs would queue on the machine
anyway, and the second would look like a hang rather than a queue.

## Where the images end up

Two destinations, answering different questions.

The bucket is the archive: every image from every build, addressed by the
commit that produced it, alongside its defconfig, its predicted config and its
manifest. Nothing expires, and a build from months ago can be reproduced or
compared.

The run's artifacts are the convenience: click and download from the Actions
page, no cloud console and no credentials. That is what you want when someone
asks for an image and the answer should be a link. Artifacts are capped at 2GB
a file and expire after the retention window, so the collection step skips
anything too large - the appliance images are 120-160MB and fit easily, while
the media and k3s images are gigabytes and go only to the bucket.

A public download page, if one is ever wanted, is a static index written
beside the artifacts listing each image with its solution hash. That is a
product decision rather than a CI one, and it can wait until there is an image
worth publishing.

## Sharing the machine without sharing a home

The builder is also the machine somebody works on. The first run failed with
"cd: /home/runner/silt: No such file or directory", because gcloud compute ssh
logs in as the local username and on a GitHub runner that is `runner`, which it
created as a fresh account on the instance.

The obvious fix - log in as the person who owns the clone - is the wrong one. A
job running as a person has that person's ssh private keys, their gcloud
credentials, and everything else in their home directory. The people who can
put code into that job are not only the repository's owner: it runs three
third-party actions, and whoever controls their tags controls what executes.

So CI keeps the account the accident created. `runner` has a clone, a scratch
directory and nothing worth stealing.

That leaves the store, which is worth sharing: it is keyed by content, so the
same image computes the same key whoever builds it, and it is twenty-seven
gigabytes of warm output trees. Giving CI its own would make every first build
half an hour. It lives in `/srv/silt`, owned by group `siltbuild`, setgid so
new directories inherit the group, with `umask 002` in silt-build.sh so what is
written stays group-writable.

```
  /home/vinodhalaharvi/        a person
      silt/                    their clone
      .ssh/  .config/gcloud/   what a CI job should not be able to read
      .cache/silt/work         their scratch tree

  /home/runner/                CI
      ci/silt/                 detached at the commit being built
      ci/work/  ci/lock        its scratch tree
                               nothing else

  /srv/silt/slots              shared, group siltbuild, setgid
```

The work directories are separate because that is scratch space, wiped at the
start of every build that is not an exact hit: two builds sharing one would
delete each other's tree. `SILT_WORK` and `SILT_LOCK` split them from the
store, and the scratch files the prefix test writes are named after the work
directory, so concurrent builds cannot overwrite each other's predicted
configs.

What they do share is the machine's cores and disk, which is a slower build
rather than a broken one.

### One-time setup on the builder

Run as the person, once. The `runner` account already exists if a CI job has
connected; `useradd` is there for the case where none has.

```sh
sudo groupadd -f siltbuild
sudo useradd -m runner 2>/dev/null || true
sudo usermod -aG siltbuild vinodhalaharvi
sudo usermod -aG siltbuild runner

sudo mkdir -p /srv/silt
sudo mv ~/.cache/silt/slots /srv/silt/slots     # same filesystem, instant
sudo chown -R vinodhalaharvi:siltbuild /srv/silt
sudo chmod -R g+rwX /srv/silt
sudo find /srv/silt -type d -exec chmod g+s {} +

# Buildroot and its downloads are read-mostly and shared as they are
sudo chgrp -R siltbuild ~/buildroot ~/.cache/buildroot-dl
sudo chmod -R g+rX ~/buildroot ~/.cache/buildroot-dl
```

Then a person builds against the shared store like this, which is worth putting
in a shell profile:

```sh
export SILT_CACHE=/srv/silt
export SILT_WORK=$HOME/.cache/silt/work
export BUILDROOT=$HOME/buildroot
```

A group change takes effect on the next login, so log out and back in before
the first build.

## What a non-interactive ssh does not have

`ssh host "command"` runs a non-interactive shell, which reads no profile and
no bashrc. Anything a login session puts on PATH is absent, and the failure is
terse: `make: go: No such file or directory`, on a machine where go is plainly
installed and works the moment you log in.

The build step exports PATH for that reason. The general form is worth keeping
in mind: anything a person's shell sets up is missing from a CI job by
construction, so if a build comes to depend on an environment variable it
belongs in the workflow rather than in somebody's bashrc.

## When ssh will not connect

The builder is reached with `gcloud compute ssh`, which needs more than
permission to start the instance. The first run failed here for five minutes
saying only "the builder did not become reachable", because the retry loop sent
the error to /dev/null - the first attempt now prints its output in a group,
and the loop is three minutes rather than five.

The usual causes, in the order they are worth checking:

- the service account cannot write ssh keys to project metadata:
  `roles/compute.instanceAdmin.v1` at the project level, or
  `roles/compute.osAdminLogin` if OS Login is enforced
- OS Login is enforced and the account has no login role: add
  `roles/compute.osAdminLogin` and `roles/iam.serviceAccountUser`
- no firewall rule allows 22 from the runner's address: set `SSH_FLAGS` to
  `--tunnel-through-iap` in the workflow and grant
  `roles/iap.tunnelResourceAccessor`

## Decisions still open

**ssh from the workflow, for now.** `gcloud compute ssh --command` is what
build.yml uses: no runner to install, nothing to keep updated on the box, and
the output streams into the Actions log as it happens. A self-hosted runner
would give nicer step boundaries and survive a dropped connection mid-build,
which is the argument for switching if a long build ever gets cut off.

**Stopping the VM when a job dies.** The workflow stops it on success, and on
failure only when `KEEP_ON_FAILURE` is not "true". It is "true" now, because a
failing run that shuts down the machine takes the evidence with it and costs a
minute to start again - and a failed build is exactly when you want to log in
and look. Set it to false once the workflow is boring, or the nightly will
leave a machine running at 3am.

Neither setting covers a cancelled run or a runner that disappears mid-job. A
watchdog on the box - stop myself if no job has touched me for an hour - is the
belt to those braces, and is worth writing before the nightly is trusted.

**What to assert beyond "it built."** `ci/silt-build.sh` already runs
`silt solve` before and `config-agrees.awk` after `make defconfig`, so every CI
build verifies the prediction against kbuild. `ci/boot-test.sh` boots a QEMU
image, which is what `(ci boot)` is for. Neither existed as a gate when an init
script ordered at S41 blocked every service behind it on a real board; a boot
test would have caught that before the card was written.
