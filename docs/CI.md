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

## Decisions still open

**Self-hosted runner, or ssh from the workflow.** A runner installed on the VM
as a service gives live logs in the Actions UI and survives restarts;
`gcloud compute ssh` from the job is fewer moving parts but streams nothing
until the command ends. Leaning runner.

**Stopping the VM when a job dies.** The workflow stops it in an `always()`
step, which covers a failed build but not a cancelled run or a runner that
disappears. A watchdog on the box — stop myself if no job has touched me for an
hour — is the belt to that braces.

**What to assert beyond "it built."** `ci/silt-build.sh` already runs
`silt solve` before and `config-agrees.awk` after `make defconfig`, so every CI
build verifies the prediction against kbuild. `ci/boot-test.sh` boots a QEMU
image, which is what `(ci boot)` is for. Neither existed as a gate when an init
script ordered at S41 blocked every service behind it on a real board; a boot
test would have caught that before the card was written.
