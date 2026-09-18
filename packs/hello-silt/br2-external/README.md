# hello-silt's br2-external tree

Buildroot reads project-specific packages, board material and configuration
from a tree outside itself, named by `BR2_EXTERNAL`. Everything here is
Silt's; the Buildroot checkout stays exactly as it was cloned.

That matters more than tidiness. Every claim Silt makes is about a specific
Buildroot release, and `silt check --buildroot` re-checks them against the
tree it is given. A tree with a package added to `package/Config.in` is no
longer that release, so the pin stops meaning anything.

    silt check --buildroot ~/buildroot --pack packs/hello-silt

`--pack` loads the fragments and this tree together, because they are two
halves of one thing: the fragment states BR2_PACKAGE_HELLO_SILT, and this tree
is what makes that symbol exist.

`hello-silt` is the smallest package that proves an image built and booted:
it prints one line, and `ci/boot-test.sh` asserts that line over the QEMU
serial console. It is a fixture, not an example of a good package.
