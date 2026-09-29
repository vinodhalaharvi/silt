# Quickstart

Five minutes, no hardware. You will end with a simulated industrial device
answering Modbus TCP, OPC UA, MQTT and HTTP on your own machine.

## Get an image

Every CI build publishes one. Take the newest run of the `build` workflow on
the Actions tab and download its artifact, or from a shell:

    gh run download --name images-<sha>

Images are zstd-compressed, and each directory carries a `README.txt` saying
what to do with it.

## Run it

`qemu-arm-appliance` is a kernel and a filesystem rather than a card image:

    zstd -d Image.zst rootfs.ext4.zst

    qemu-system-aarch64 -M virt -cpu cortex-a53 -m 512 -nographic \
      -kernel Image \
      -drive file=rootfs.ext4,if=none,id=hd,format=raw \
      -device virtio-blk-device,drive=hd \
      -append "root=/dev/vda rootwait console=ttyAMA0" \
      -netdev user,id=n,hostfwd=tcp::5020-:502,hostfwd=tcp::4840-:4840,hostfwd=tcp::8080-:8080 \
      -device virtio-net-device,netdev=n

Log in as `root`, no password. Leave with `Ctrl-A` then `X`.

## Talk to it

From another terminal, against localhost:

    curl -s http://localhost:8080/tags
    curl -s http://localhost:8080/health

Modbus, if you have `mbpoll` (`brew install mbpoll`):

    mbpoll -a 1 -t 4 -r 1 -p 5020 127.0.0.1          # read the setpoint
    mbpoll -a 1 -t 4 -r 1 -p 5020 127.0.0.1 620      # write 62.0
    curl -s http://localhost:8080/tags | grep setpoint

The write arrives on OPC UA and HTTP too, because every daemon reads one shared
tag table in `/dev/shm`. That is the whole design: one device, several
protocols, no translation layer.

## Change the device

    cat /etc/silt-sim.json

One file describes the whole device: each tag, how it moves, and where it
appears on each protocol. Edit it, restart, and the device is different -
nothing about the device is compiled into the image.

## Build it yourself

    git clone https://github.com/vinodhalaharvi/silt
    cd silt && make
    ./bin/silt check --buildroot /path/to/buildroot-2025.02.16
    ./bin/silt emit images/qemu-arm-appliance.sx -o ~/out --buildroot /path/to/buildroot

See [Building](Building) for the cache, which turns a repeat build into three
seconds.
