# Compare a predicted .config against the one kbuild wrote.
#
# Only symbols kbuild knew about are compared: silt loads every pack, while
# make is given only the externals this image needs, so the prediction
# legitimately mentions symbols that do not exist in this build's universe.
# Everything kbuild did know about must match, set or unset alike.
# Symbols kbuild fills in from the invocation rather than from the
# configuration: where the defconfig was read from, which externals were on
# the command line, where the kernel fragment lives. They differ by
# construction and say nothing about whether the model is right.
function environment(sym) {
    return sym == "BR2_DEFCONFIG" ||
           sym == "BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES" ||
           sym == "BR2_DL_DIR" || sym == "BR2_CCACHE_DIR" || sym == "BR2_JLEVEL" ||
           sym ~ /^BR2_EXTERNAL/
}

FNR == NR {
    if ($0 ~ /^[A-Za-z0-9_]+=/) { split($0, a, "="); real[a[1]] = substr($0, index($0, "=") + 1); known[a[1]] = 1 }
    else if ($0 ~ /^# [A-Za-z0-9_]+ is not set$/) { real[$2] = "n"; known[$2] = 1 }
    next
}
$0 ~ /^[A-Za-z0-9_]+=/ {
    split($0, a, "="); sym = a[1]; val = substr($0, index($0, "=") + 1)
    if (!(sym in known) || environment(sym)) next
    if (real[sym] != val) { printf "  %s: silt says %s, kbuild wrote %s\n", sym, val, real[sym]; bad++ }
}
$0 ~ /^# [A-Za-z0-9_]+ is not set$/ {
    sym = $2
    if (!(sym in known) || environment(sym)) next
    if (real[sym] != "n") { printf "  %s: silt says n, kbuild wrote %s\n", sym, real[sym]; bad++ }
}
END { if (bad) { printf "%d disagreement(s)\n", bad; exit 1 } else print "prediction and kbuild agree on every symbol kbuild knew" }
